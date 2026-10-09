// Package outboxdispatch publishes pending transactional-outbox rows and marks
// them published only after a broker confirmation.
package outboxdispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

const (
	defaultBatchSize    = int32(20)
	defaultPollInterval = time.Second
	publishErrorMessage = "broker publish failed"
)

// Publisher is implemented by the RabbitMQ wrapper. Success means the broker
// publisher confirmation has been received.
type Publisher interface {
	Publish(context.Context, string, []byte) error
}

type txStore interface {
	ListPending(context.Context, int32) ([]db.OutboxEvent, error)
	MarkPublished(context.Context, pgtype.UUID, time.Time) error
	RecordAttempt(context.Context, pgtype.UUID, string) error
	Commit(context.Context) error
	Rollback(context.Context) error
}

type txFactory interface {
	Begin(context.Context) (txStore, error)
}

// Database is the transaction surface implemented by *pgxpool.Pool.
type Database interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Dispatcher struct {
	factory      txFactory
	publisher    Publisher
	batchSize    int32
	pollInterval time.Duration
}

// New constructs a PostgreSQL-backed dispatcher.
func New(database Database, publisher Publisher) *Dispatcher {
	return &Dispatcher{
		factory:      postgresFactory{database: database},
		publisher:    publisher,
		batchSize:    defaultBatchSize,
		pollInterval: defaultPollInterval,
	}
}

// Run polls continuously until cancellation.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	for {
		processed, err := d.DispatchOnce(ctx)
		if err != nil && ctx.Err() == nil {
			if err := wait(ctx, d.pollInterval); err != nil {
				return nil
			}
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if processed == 0 {
			if err := wait(ctx, d.pollInterval); err != nil {
				return nil
			}
		}
	}
}

// DispatchOnce locks one pending batch. The database transaction deliberately
// remains open through publish confirmation so concurrent dispatchers cannot
// select the same rows. A commit failure after confirmation is safe: the row
// remains pending and its stable message ID makes the duplicate recognizable.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if err := d.validate(); err != nil {
		return 0, err
	}
	tx, err := d.factory.Begin(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	events, err := tx.ListPending(ctx, d.batchSize)
	if err != nil {
		return 0, err
	}
	processed := 0
	publishFailed := false
	for _, event := range events {
		payload, err := encodeMessage(event)
		if err != nil {
			return processed, err
		}
		if err := d.publisher.Publish(ctx, event.ID.String(), payload); err != nil {
			if recordErr := tx.RecordAttempt(ctx, event.ID, safePublishError(err)); recordErr != nil {
				return processed, recordErr
			}
			processed++
			publishFailed = true
			continue
		}
		if err := tx.MarkPublished(ctx, event.ID, time.Now().UTC()); err != nil {
			return processed, err
		}
		processed++
	}
	if err := tx.Commit(ctx); err != nil {
		return processed, err
	}
	committed = true
	if publishFailed {
		return processed, errors.New(publishErrorMessage)
	}
	return processed, nil
}

func (d *Dispatcher) validate() error {
	if d == nil || d.factory == nil {
		return errors.New("outbox dispatcher database is not configured")
	}
	if d.publisher == nil {
		return errors.New("outbox dispatcher publisher is not configured")
	}
	if d.batchSize <= 0 {
		return errors.New("outbox dispatcher batch size must be positive")
	}
	return nil
}

type messagePayload struct {
	CaseID        string `json:"case_id"`
	AnalysisID    string `json:"analysis_id"`
	OutboxEventID string `json:"outbox_event_id"`
	EventType     string `json:"event_type"`
}

func encodeMessage(event db.OutboxEvent) ([]byte, error) {
	if !event.ID.Valid || !event.CaseID.Valid || !event.AnalysisID.Valid || event.EventType == "" {
		return nil, errors.New("outbox event has invalid delivery identifiers")
	}
	payload, err := json.Marshal(messagePayload{
		CaseID:        event.CaseID.String(),
		AnalysisID:    event.AnalysisID.String(),
		OutboxEventID: event.ID.String(),
		EventType:     event.EventType,
	})
	if err != nil {
		return nil, fmt.Errorf("encode outbox event: %w", err)
	}
	return payload, nil
}

func safePublishError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "broker publish canceled"
	}
	// Broker errors may contain endpoint or protocol details. Persist only a
	// stable operational category; correlation belongs in structured logs.
	return publishErrorMessage
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type postgresFactory struct{ database Database }

func (f postgresFactory) Begin(ctx context.Context) (txStore, error) {
	if f.database == nil {
		return nil, errors.New("outbox dispatcher database is not configured")
	}
	tx, err := f.database.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &postgresTx{tx: tx, queries: db.New(tx)}, nil
}

type postgresTx struct {
	tx      pgx.Tx
	queries *db.Queries
}

func (t *postgresTx) ListPending(ctx context.Context, limit int32) ([]db.OutboxEvent, error) {
	return t.queries.ListPendingOutboxEventsForUpdate(ctx, limit)
}

func (t *postgresTx) MarkPublished(ctx context.Context, id pgtype.UUID, at time.Time) error {
	_, err := t.queries.MarkOutboxEventPublished(ctx, db.MarkOutboxEventPublishedParams{
		ID: id, PublishedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	return err
}

func (t *postgresTx) RecordAttempt(ctx context.Context, id pgtype.UUID, message string) error {
	_, err := t.queries.RecordOutboxAttempt(ctx, db.RecordOutboxAttemptParams{
		ID: id, LastError: pgtype.Text{String: message, Valid: true},
	})
	return err
}

func (t *postgresTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *postgresTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
