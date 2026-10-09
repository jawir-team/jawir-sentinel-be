// Package rabbitmq provides the small, durable AMQP surface used by the
// outbox dispatcher and AI worker.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	minReconnectBackoff = 250 * time.Millisecond
	maxReconnectBackoff = 5 * time.Second
)

// Message is the broker-neutral delivery shape used by worker handlers.
type Message struct {
	Body        []byte
	MessageID   string
	Redelivered bool
}

// Handler returns nil only after the delivery is durably handled. A non-nil
// error causes a negative acknowledgement with requeue=true.
type Handler func(context.Context, Message) error

// Broker owns reconnectable AMQP connections. Publishing uses a fresh confirm
// channel per call; consuming uses a manual-ack channel with prefetch one.
type Broker struct {
	url   string
	queue string

	connMu    sync.Mutex
	conn      *amqp.Connection
	publishMu sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
}

// Dial validates the settings, connects, and idempotently declares a durable
// quorum queue.
func Dial(ctx context.Context, url, queue string) (*Broker, error) {
	url = strings.TrimSpace(url)
	queue = strings.TrimSpace(queue)
	if url == "" {
		return nil, errors.New("rabbitmq: URL is required")
	}
	if queue == "" {
		return nil, errors.New("rabbitmq: queue is required")
	}
	b := &Broker{url: url, queue: queue, closed: make(chan struct{})}
	if _, err := b.connection(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

// Close interrupts reconnect loops and closes the current AMQP connection.
func (b *Broker) Close() error {
	if b == nil {
		return nil
	}
	var closeErr error
	b.closeOnce.Do(func() {
		close(b.closed)
		b.connMu.Lock()
		defer b.connMu.Unlock()
		if b.conn != nil && !b.conn.IsClosed() {
			closeErr = b.conn.Close()
		}
		b.conn = nil
	})
	return closeErr
}

// Publish sends a persistent message through the default exchange and waits
// for the publisher confirmation before returning success.
func (b *Broker) Publish(ctx context.Context, messageID string, body []byte) error {
	if b == nil {
		return errors.New("rabbitmq: broker is not configured")
	}
	if strings.TrimSpace(messageID) == "" {
		return errors.New("rabbitmq: message ID is required")
	}

	b.publishMu.Lock()
	defer b.publishMu.Unlock()

	conn, err := b.connection(ctx)
	if err != nil {
		return err
	}
	ch, err := b.channel(conn)
	if err != nil {
		b.invalidate(conn)
		return fmt.Errorf("rabbitmq: open publish channel: %w", err)
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return fmt.Errorf("rabbitmq: enable publisher confirms: %w", err)
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := ch.NotifyReturn(make(chan amqp.Return, 1))

	err = ch.PublishWithContext(ctx, "", b.queue, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    messageID,
		Body:         append([]byte(nil), body...),
	})
	if err != nil {
		return fmt.Errorf("rabbitmq: publish: %w", err)
	}

	for {
		select {
		case returned := <-returns:
			return fmt.Errorf("rabbitmq: message was returned (%d %s)", returned.ReplyCode, returned.ReplyText)
		case confirmation, ok := <-confirms:
			if !ok {
				return errors.New("rabbitmq: confirm channel closed before acknowledgement")
			}
			if !confirmation.Ack {
				return errors.New("rabbitmq: publisher negative acknowledgement")
			}
			// RabbitMQ sends basic.return before basic.ack for an unroutable
			// mandatory publish. Drain the already-delivered return before
			// accepting the confirmation.
			select {
			case returned := <-returns:
				return fmt.Errorf("rabbitmq: message was returned (%d %s)", returned.ReplyCode, returned.ReplyText)
			default:
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-b.closed:
			return errors.New("rabbitmq: broker is closed")
		}
	}
}

// Consume runs until the context is cancelled. AMQP/session failures are
// retried with bounded backoff. The handler controls acknowledgement through
// its return value.
func (b *Broker) Consume(ctx context.Context, handler Handler) error {
	if b == nil {
		return errors.New("rabbitmq: broker is not configured")
	}
	if handler == nil {
		return errors.New("rabbitmq: delivery handler is required")
	}

	backoff := minReconnectBackoff
	for {
		err := b.consumeSession(ctx, handler)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-b.closed:
			return nil
		default:
		}
		if err == nil {
			backoff = minReconnectBackoff
		}
		if err := wait(ctx, b.closed, backoff); err != nil {
			return nil
		}
		backoff *= 2
		if backoff > maxReconnectBackoff {
			backoff = maxReconnectBackoff
		}
	}
}

func (b *Broker) consumeSession(ctx context.Context, handler Handler) error {
	conn, err := b.connection(ctx)
	if err != nil {
		return err
	}
	ch, err := b.channel(conn)
	if err != nil {
		b.invalidate(conn)
		return fmt.Errorf("rabbitmq: open consume channel: %w", err)
	}
	defer ch.Close()
	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("rabbitmq: set consumer prefetch: %w", err)
	}
	deliveries, err := ch.Consume(b.queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("rabbitmq: consume: %w", err)
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.closed:
			return nil
		case amqpErr := <-closed:
			b.invalidate(conn)
			if amqpErr == nil {
				return errors.New("rabbitmq: consumer channel closed")
			}
			return fmt.Errorf("rabbitmq: consumer channel closed: %w", amqpErr)
		case delivery, ok := <-deliveries:
			if !ok {
				b.invalidate(conn)
				return errors.New("rabbitmq: delivery channel closed")
			}
			message := Message{Body: append([]byte(nil), delivery.Body...), MessageID: delivery.MessageId, Redelivered: delivery.Redelivered}
			if err := handler(ctx, message); err != nil {
				if nackErr := delivery.Nack(false, true); nackErr != nil {
					return fmt.Errorf("rabbitmq: handler failed (%v) and nack failed: %w", err, nackErr)
				}
				if err := wait(ctx, b.closed, minReconnectBackoff); err != nil {
					return nil
				}
				continue
			}
			if err := delivery.Ack(false); err != nil {
				return fmt.Errorf("rabbitmq: acknowledge delivery: %w", err)
			}
		}
	}
}

func (b *Broker) connection(ctx context.Context) (*amqp.Connection, error) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn != nil && !b.conn.IsClosed() {
		return b.conn, nil
	}
	select {
	case <-b.closed:
		return nil, errors.New("rabbitmq: broker is closed")
	default:
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := amqp.DialConfig(b.url, amqp.Config{
		Dial: func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	})
	if err != nil {
		return nil, errors.New("rabbitmq: connection failed")
	}
	ch, err := b.channel(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = ch.Close()
	b.conn = conn
	return conn, nil
}

func (b *Broker) channel(conn *amqp.Connection) (*amqp.Channel, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	_, err = ch.QueueDeclare(b.queue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"})
	if err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("declare durable quorum queue %q: %w", b.queue, err)
	}
	return ch, nil
}

func (b *Broker) invalidate(conn *amqp.Connection) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn == conn {
		b.conn = nil
		_ = conn.Close()
	}
}

func wait(ctx context.Context, closed <-chan struct{}, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-closed:
		return errors.New("rabbitmq: broker is closed")
	}
}
