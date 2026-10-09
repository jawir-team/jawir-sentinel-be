package aiconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/rabbitmq"
)

func TestProcessorClaimRotationAndSuccess(t *testing.T) {
	worker := newFakeWorker()
	worker.active = true
	worker.stale = true
	oldAttempt := worker.attempt
	executor := &fakeExecutor{results: []executionResult{{result: Execution{Success: &aiworker.SuccessResult{}}}}}

	if err := New(worker, executor, 3).Handle(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if worker.attempt == oldAttempt || worker.status != "COMPLETED" || executor.calls != 1 {
		t.Fatalf("rotated attempt/status/calls = %v/%s/%d", worker.attempt, worker.status, executor.calls)
	}
}

func TestProcessorAcknowledgedNoOps(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		active bool
	}{
		{name: "finalized", status: "FAILED"},
		{name: "active duplicate", status: "GENERATING", active: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := newFakeWorker()
			worker.status = test.status
			worker.active = test.active
			executor := &fakeExecutor{}
			if err := New(worker, executor, 3).Handle(context.Background(), testMessage()); err != nil {
				t.Fatal(err)
			}
			if executor.calls != 0 || worker.finalizeCalls != 0 {
				t.Fatalf("executor/finalize calls = %d/%d", executor.calls, worker.finalizeCalls)
			}
		})
	}
}

func TestProcessorFencesSupersededAttempt(t *testing.T) {
	worker := newFakeWorker()
	executor := &fakeExecutor{results: []executionResult{{result: Execution{Success: &aiworker.SuccessResult{}}}}}
	executor.beforeReturn = func() { worker.rotate() }

	err := New(worker, executor, 3).Handle(context.Background(), testMessage())
	if !errors.Is(err, aiworker.ErrStaleClaim) {
		t.Fatalf("Handle() error = %v, want stale claim", err)
	}
	if worker.status != "GENERATING" || worker.retryCount != 0 {
		t.Fatalf("superseded worker mutated state: status=%s retries=%d", worker.status, worker.retryCount)
	}
}

func TestProcessorTechnicalRetrySurvivesRotation(t *testing.T) {
	worker := newFakeWorker()
	worker.retryCount = 1
	executor := &fakeExecutor{results: []executionResult{
		{err: errors.New("vertex unavailable")},
		{result: Execution{Success: &aiworker.SuccessResult{}}},
	}}

	if err := New(worker, executor, 3).Handle(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if worker.retryCount != 2 || worker.status != "COMPLETED" || worker.retryCalls != 1 {
		t.Fatalf("retry count/status/calls = %d/%s/%d", worker.retryCount, worker.status, worker.retryCalls)
	}
}

func TestProcessorFinalizationFailureRequestsRedelivery(t *testing.T) {
	worker := newFakeWorker()
	worker.finalizeErr = errors.New("commit failed")
	executor := &fakeExecutor{results: []executionResult{{result: Execution{Success: &aiworker.SuccessResult{}}}}}
	if err := New(worker, executor, 3).Handle(context.Background(), testMessage()); !errors.Is(err, worker.finalizeErr) {
		t.Fatalf("Handle() error = %v", err)
	}
	if worker.status != "GENERATING" {
		t.Fatalf("status = %s, want GENERATING", worker.status)
	}
}

func TestProcessorDoesNotClaimWithoutExecutor(t *testing.T) {
	worker := newFakeWorker()
	err := New(worker, nil, 3).Handle(context.Background(), testMessage())
	if !errors.Is(err, ErrExecutorNotConfigured) || worker.claimCalls != 0 {
		t.Fatalf("error/claim calls = %v/%d", err, worker.claimCalls)
	}
}

type executionResult struct {
	result Execution
	err    error
}

type fakeExecutor struct {
	results      []executionResult
	calls        int
	beforeReturn func()
}

func (f *fakeExecutor) Execute(context.Context, Job) (Execution, error) {
	index := f.calls
	f.calls++
	if f.beforeReturn != nil {
		f.beforeReturn()
		f.beforeReturn = nil
	}
	if index >= len(f.results) {
		return Execution{}, errors.New("unexpected execution")
	}
	return f.results[index].result, f.results[index].err
}

type fakeWorker struct {
	status        string
	attempt       pgtype.UUID
	sequence      byte
	retryCount    int32
	active        bool
	stale         bool
	claimCalls    int
	retryCalls    int
	finalizeCalls int
	finalizeErr   error
}

func newFakeWorker() *fakeWorker {
	f := &fakeWorker{status: "GENERATING"}
	f.rotate()
	f.active = false
	return f
}

func (f *fakeWorker) rotate() {
	f.sequence++
	f.attempt = testUUID(f.sequence + 20)
	f.active = true
	f.stale = false
}

func (f *fakeWorker) Claim(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) (aiworker.ClaimResult, error) {
	f.claimCalls++
	if f.status == "COMPLETED" || f.status == "FAILED" {
		return aiworker.ClaimResult{Outcome: aiworker.AlreadyFinalized, WorkerAttemptID: f.attempt, TechnicalRetryCount: f.retryCount}, nil
	}
	if f.active && !f.stale {
		return aiworker.ClaimResult{Outcome: aiworker.Duplicate, WorkerAttemptID: f.attempt, TechnicalRetryCount: f.retryCount}, nil
	}
	f.rotate()
	return aiworker.ClaimResult{Outcome: aiworker.Claimed, WorkerAttemptID: f.attempt, TechnicalRetryCount: f.retryCount}, nil
}

func (f *fakeWorker) FinalizeSuccess(_ context.Context, _, _, attempt pgtype.UUID, _ aiworker.SuccessResult) (aiworker.Outcome, error) {
	return f.finalize(attempt, "COMPLETED")
}

func (f *fakeWorker) VerifierFail(_ context.Context, _, _, attempt pgtype.UUID, _ aiworker.FailedCandidate) (aiworker.Outcome, error) {
	return f.finalize(attempt, "FAILED")
}

func (f *fakeWorker) TechnicalExhaustion(_ context.Context, _, _, attempt pgtype.UUID) (aiworker.Outcome, error) {
	return f.finalize(attempt, "FAILED")
}

func (f *fakeWorker) TechnicalRetry(_ context.Context, _ pgtype.UUID, attempt pgtype.UUID) (db.AiAnalysis, error) {
	if attempt != f.attempt || f.status != "GENERATING" {
		return db.AiAnalysis{}, aiworker.ErrStaleClaim
	}
	f.retryCalls++
	f.retryCount++
	f.rotate()
	return db.AiAnalysis{WorkerAttemptID: f.attempt, TechnicalRetryCount: f.retryCount}, nil
}

func (f *fakeWorker) finalize(attempt pgtype.UUID, status string) (aiworker.Outcome, error) {
	f.finalizeCalls++
	if attempt != f.attempt || f.status != "GENERATING" {
		return "", aiworker.ErrStaleClaim
	}
	if f.finalizeErr != nil {
		return "", f.finalizeErr
	}
	f.status = status
	return aiworker.Finalized, nil
}

func testMessage() rabbitmq.Message {
	payload := wirePayload{
		CaseID: testUUID(1).String(), AnalysisID: testUUID(2).String(),
		OutboxEventID: testUUID(3).String(), EventType: EventAnalysisRequested,
	}
	body, _ := json.Marshal(payload)
	return rabbitmq.Message{Body: body, MessageID: payload.OutboxEventID}
}

func testUUID(last byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{6: 0x40, 8: 0x80, 15: last}, Valid: true}
}
