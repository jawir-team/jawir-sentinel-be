//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiconsumer"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/outboxdispatch"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

func TestPublisherUnavailableRetriesPendingEventExactlyOnce(t *testing.T) {
	ctx := context.Background(); store, _, _, event := seededAIJob()
	publisher := &capturePublisher{failures: 1}
	dispatcher := outboxdispatch.NewWithFactory(memoryOutboxFactory{store: store}, publisher)
	if processed, err := dispatcher.DispatchOnce(ctx); processed != 1 || err == nil { t.Fatalf("first poll=(%d,%v), want one failed attempt", processed, err) }
	row := store.outboxes[event.ID]
	if row.Status != "PENDING" || row.AttemptCount != 1 || len(publisher.messages) != 0 { t.Fatalf("after failure=%+v messages=%d", row, len(publisher.messages)) }
	if processed, err := dispatcher.DispatchOnce(ctx); processed != 1 || err != nil { t.Fatalf("second poll=(%d,%v), want success", processed, err) }
	if processed, err := dispatcher.DispatchOnce(ctx); processed != 0 || err != nil { t.Fatalf("third poll=(%d,%v), want empty", processed, err) }
	if publisher.calls != 2 || len(publisher.messages) != 1 || store.outboxes[event.ID].Status != "PUBLISHED" {
		t.Fatalf("publisher calls=%d deliveries=%d outbox=%+v", publisher.calls, len(publisher.messages), store.outboxes[event.ID])
	}
}

func TestDuplicateDeliveryDoesNotDoubleFinalize(t *testing.T) {
	ctx := context.Background(); store, _, _, event := seededAIJob()
	publisher := &capturePublisher{}; dispatcher := outboxdispatch.NewWithFactory(memoryOutboxFactory{store: store}, publisher)
	if _, err := dispatcher.DispatchOnce(ctx); err != nil { t.Fatal(err) }
	executor := &staticExecutor{execution: successExecution()}; processor := aiconsumer.New(aiworker.NewWithStore(store, 30), executor, 0)
	message := publisher.messages[0]
	if err := processor.Handle(ctx, message); err != nil { t.Fatal(err) }
	if err := processor.Handle(ctx, message); err != nil { t.Fatal(err) }
	if executor.calls != 1 || store.finalizeRuns != 1 { t.Fatalf("executor=%d finalize=%d want=1/1", executor.calls, store.finalizeRuns) }
	completed := 0; for _, audit := range store.audits { if audit.EventType == "AI_ANALYSIS_COMPLETED" { completed++ } }
	if completed != 1 || store.outboxes[event.ID].Status != "PUBLISHED" { t.Fatalf("completion audits=%d outbox=%+v", completed, store.outboxes[event.ID]) }
}

func TestVerifierFailureIsPersistedAndAudited(t *testing.T) {
	ctx := context.Background(); store, _, analysis, event := seededAIJob()
	publisher := &capturePublisher{}; dispatcher := outboxdispatch.NewWithFactory(memoryOutboxFactory{store: store}, publisher)
	if _, err := dispatcher.DispatchOnce(ctx); err != nil { t.Fatal(err) }
	failure := aiworker.FailedCandidate{Candidate: validCandidate(), Verification: ai.VerificationResult{
		Status: ai.VerificationStatusFail,
		Issues: []ai.VerifierIssue{{Type: ai.VerifierIssueHallucinatedEvidence, Severity: ai.IssueSeverityCritical, Description: "unknown evidence"}},
	}}
	executor := &staticExecutor{execution: aiconsumer.Execution{VerifierFailure: &failure}}
	if err := aiconsumer.New(aiworker.NewWithStore(store, 30), executor, 0).Handle(ctx, publisher.messages[0]); err != nil { t.Fatal(err) }
	got := store.analyses[analysis.ID]
	if got.Status != "FAILED" || got.VerificationStatus.String != "FAIL" || store.cases[analysis.CaseID].Status != "ESCALATION_REQUIRED" {
		t.Fatalf("failed analysis=%+v case=%+v", got, store.cases[analysis.CaseID])
	}
	if len(store.audits) != 1 || store.audits[0].EventType != "VERIFIER_FAIL" || !sameID(store.audits[0].AnalysisID, analysis.ID) {
		t.Fatalf("audits=%+v event=%+v", store.audits, event)
	}
}

func TestQuotaExhaustionEscalatesWithoutNewAnalysisOrOutbox(t *testing.T) {
	caseID, analysisID := testUUID(40), testUUID(41)
	runner := &quotaRunner{caseRow: db.Case{ID: caseID, Status: string(workflow.StateSigning), CurrentAnalysisID: analysisID}, latest: 4}
	service := reanalysis.NewWithRunner(runner, 3)
	result, err := service.Run(context.Background(), reanalysis.Request{CaseID: caseID, Trigger: workflow.EventSignerRejected,
		ActorID: testUUID(42), ActorRole: "SIGNER"}, func(context.Context, db.DBTX, db.Case) error { runner.actions++; return nil })
	if err != nil { t.Fatal(err) }
	if result.Outcome != reanalysis.LimitReached || result.Case.Status != "ESCALATION_REQUIRED" { t.Fatalf("result=%+v", result) }
	if runner.actions != 1 || runner.createdAnalyses != 0 || runner.createdOutboxes != 0 { t.Fatalf("actions=%d analyses=%d outboxes=%d", runner.actions, runner.createdAnalyses, runner.createdOutboxes) }
	if len(runner.audits) != 1 || runner.audits[0].EventType != "REANALYSIS_LIMIT_REACHED" || !sameID(runner.audits[0].AnalysisID, analysisID) {
		t.Fatalf("audits=%+v", runner.audits)
	}
}

func TestCloseRejectsGeneratingAnalysisAndRunningExecution(t *testing.T) {
	for _, tc := range []struct{name string; setup func(*memoryStore, db.Case)}{
		{name: "generating analysis", setup: func(s *memoryStore, c db.Case) { s.analyses[testUUID(62)] = db.AiAnalysis{ID: testUUID(62), CaseID: c.ID, Status: "GENERATING"} }},
		{name: "in progress execution", setup: func(s *memoryStore, c db.Case) { s.executions[testUUID(63)] = db.Execution{ID: testUUID(63), CaseID: c.ID, Status: "IN_PROGRESS"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore(); maker := user(60, auth.SystemRoleUser); seedUsers(store, maker)
			c := db.Case{ID: testUUID(61), CaseTypeID: store.caseType.ID, Title: "active", Status: "ESCALATION_REQUIRED", OwnerID: maker.ID, CreatedBy: maker.ID}
			store.cases[c.ID] = c; store.participants[testUUID(64)] = db.CaseParticipant{ID: testUUID(64), CaseID: c.ID, UserID: maker.ID, Role: "MAKER", Status: "ACTIVE"}; tc.setup(store, c)
			serve(t, handler.CloseCase(store), request(t, http.MethodPost, "/close", map[string]any{"reason": "stop"}, maker,
				map[string]string{"id": c.ID.String()}), http.StatusConflict)
			if store.cases[c.ID].Status == "CLOSED" || len(store.audits) != 0 { t.Fatalf("closure escaped guard: case=%+v audits=%+v", store.cases[c.ID], store.audits) }
		})
	}
}

func TestStaleWorkerLeaseCannotFinalize(t *testing.T) {
	ctx := context.Background(); store, c, analysis, event := seededAIJob(); worker := aiworker.NewWithStore(store, 30)
	first, err := worker.Claim(ctx, c.ID, analysis.ID, event.ID); if err != nil { t.Fatal(err) }
	store.claimActive = false
	second, err := worker.Claim(ctx, c.ID, analysis.ID, event.ID); if err != nil { t.Fatal(err) }
	if sameID(first.WorkerAttemptID, second.WorkerAttemptID) { t.Fatal("lease was not rotated") }
	if _, err := worker.FinalizeSuccess(ctx, c.ID, analysis.ID, first.WorkerAttemptID, *successExecution().Success); !errors.Is(err, aiworker.ErrStaleClaim) {
		t.Fatalf("old FinalizeSuccess error=%v want ErrStaleClaim", err)
	}
	if store.analyses[analysis.ID].Status != "GENERATING" || store.finalizeRuns != 0 { t.Fatalf("stale attempt mutated analysis=%+v finalize=%d", store.analyses[analysis.ID], store.finalizeRuns) }
	if outcome, err := worker.FinalizeSuccess(ctx, c.ID, analysis.ID, second.WorkerAttemptID, *successExecution().Success); err != nil || outcome != aiworker.Finalized {
		t.Fatalf("current FinalizeSuccess=(%s,%v)", outcome, err)
	}
}

func seededAIJob() (*memoryStore, db.Case, db.AiAnalysis, db.OutboxEvent) {
	s := newMemoryStore(); c := db.Case{ID: testUUID(20), CaseTypeID: s.caseType.ID, Title: "seed", Status: "AI_ANALYSIS", OwnerID: testUUID(21), CreatedBy: testUUID(21)}; s.cases[c.ID] = c
	a := db.AiAnalysis{ID: testUUID(22), CaseID: c.ID, Version: 1, Status: "GENERATING", CreatedAt: s.tick()}; s.analyses[a.ID] = a
	e := db.OutboxEvent{ID: testUUID(23), CaseID: c.ID, AnalysisID: a.ID, EventType: "AI_ANALYSIS_REQUESTED", Status: "PENDING", CreatedAt: s.tick()}; s.outboxes[e.ID] = e
	return s, c, a, e
}

type quotaRunner struct {
	caseRow db.Case; latest int32; actions, createdAnalyses, createdOutboxes int; audits []db.AuditEvent
}
func (r *quotaRunner) Run(ctx context.Context, fn func(context.Context, reanalysis.Queries, db.DBTX) error) error { return fn(ctx, r, nil) }
func (r *quotaRunner) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) { return r.caseRow, nil }
func (r *quotaRunner) UpdateCaseStatus(_ context.Context, p db.UpdateCaseStatusParams) (db.Case, error) { r.caseRow.Status = p.Status; return r.caseRow, nil }
func (r *quotaRunner) LockAnalysisVersionSeq(context.Context, pgtype.UUID) error { return nil }
func (r *quotaRunner) MaxAnalysisVersion(context.Context, pgtype.UUID) (int32, error) { return r.latest, nil }
func (r *quotaRunner) CreateAnalysis(context.Context, db.CreateAnalysisParams) (db.AiAnalysis, error) { r.createdAnalyses++; return db.AiAnalysis{}, nil }
func (r *quotaRunner) CreateOutboxEvent(context.Context, db.CreateOutboxEventParams) (db.OutboxEvent, error) { r.createdOutboxes++; return db.OutboxEvent{}, nil }
func (r *quotaRunner) AppendCaseAuditEvent(_ context.Context, p db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	v := db.AuditEvent{ID: p.ID, CaseID: p.CaseID, EventType: p.EventType, AnalysisID: p.AnalysisID, Metadata: p.Metadata}; r.audits = append(r.audits, v); return v, nil
}

