package outboxdispatch

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

func TestDispatchPublishThenMarkOrdering(t *testing.T) {
	var calls []string
	tx := &fakeTx{events: []db.OutboxEvent{testEvent(1)}, calls: &calls}
	dispatcher := testDispatcher(tx, &fakePublisher{calls: &calls})

	processed, err := dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	want := []string{"list", "publish:00000000-0000-4000-8000-000000000001", "mark:00000000-0000-4000-8000-000000000001", "commit"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDispatchPublishFailureRecordsSafeAttemptWithoutMark(t *testing.T) {
	var calls []string
	tx := &fakeTx{events: []db.OutboxEvent{testEvent(1)}, calls: &calls}
	dispatcher := testDispatcher(tx, &fakePublisher{calls: &calls, err: errors.New("amqp://user:secret@example")})

	if _, err := dispatcher.DispatchOnce(context.Background()); err == nil || err.Error() != publishErrorMessage {
		t.Fatalf("DispatchOnce() error = %v", err)
	}
	want := []string{"list", "publish:00000000-0000-4000-8000-000000000001", "attempt:broker publish failed", "commit"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDispatchMarkFailureRollsBackAfterConfirmedPublish(t *testing.T) {
	var calls []string
	tx := &fakeTx{events: []db.OutboxEvent{testEvent(1)}, calls: &calls, markErr: errors.New("database unavailable")}
	dispatcher := testDispatcher(tx, &fakePublisher{calls: &calls})

	if _, err := dispatcher.DispatchOnce(context.Background()); !errors.Is(err, tx.markErr) {
		t.Fatalf("DispatchOnce() error = %v", err)
	}
	want := []string{"list", "publish:00000000-0000-4000-8000-000000000001", "mark:00000000-0000-4000-8000-000000000001", "rollback"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

type fakePublisher struct {
	calls *[]string
	err   error
}

func (p *fakePublisher) Publish(_ context.Context, id string, _ []byte) error {
	*p.calls = append(*p.calls, "publish:"+id)
	return p.err
}

type fakeFactory struct{ tx TxStore }

func (f fakeFactory) Begin(context.Context) (TxStore, error) { return f.tx, nil }

type fakeTx struct {
	events  []db.OutboxEvent
	calls   *[]string
	markErr error
}

func (f *fakeTx) ListPending(context.Context, int32) ([]db.OutboxEvent, error) {
	*f.calls = append(*f.calls, "list")
	return f.events, nil
}
func (f *fakeTx) MarkPublished(_ context.Context, id pgtype.UUID, _ time.Time) error {
	*f.calls = append(*f.calls, "mark:"+id.String())
	return f.markErr
}
func (f *fakeTx) RecordAttempt(_ context.Context, _ pgtype.UUID, message string) error {
	*f.calls = append(*f.calls, "attempt:"+message)
	return nil
}
func (f *fakeTx) Commit(context.Context) error {
	*f.calls = append(*f.calls, "commit")
	return nil
}
func (f *fakeTx) Rollback(context.Context) error {
	*f.calls = append(*f.calls, "rollback")
	return nil
}

func testDispatcher(tx TxStore, publisher Publisher) *Dispatcher {
	return &Dispatcher{factory: fakeFactory{tx: tx}, publisher: publisher, batchSize: 20, pollInterval: time.Millisecond}
}

func testEvent(last byte) db.OutboxEvent {
	return db.OutboxEvent{
		ID:         pgtype.UUID{Bytes: [16]byte{6: 0x40, 8: 0x80, 15: last}, Valid: true},
		CaseID:     pgtype.UUID{Bytes: [16]byte{6: 0x40, 8: 0x80, 14: 1, 15: last}, Valid: true},
		AnalysisID: pgtype.UUID{Bytes: [16]byte{6: 0x40, 8: 0x80, 14: 2, 15: last}, Valid: true},
		EventType:  "AI_ANALYSIS_REQUESTED",
	}
}
