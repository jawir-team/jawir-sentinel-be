package reanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

type fakeState struct {
	caseRow         db.Case
	businessActions int
	analyses        []db.AiAnalysis
	audits          []db.AuditEvent
	outboxes        []db.OutboxEvent
	statusUpdates   []string
}

func (s fakeState) clone() fakeState {
	s.analyses = append([]db.AiAnalysis(nil), s.analyses...)
	s.audits = append([]db.AuditEvent(nil), s.audits...)
	s.outboxes = append([]db.OutboxEvent(nil), s.outboxes...)
	s.statusUpdates = append([]string(nil), s.statusUpdates...)
	return s
}

type fakeRunner struct {
	state        fakeState
	latest       int32
	outboxErr    error
	commitErr    error
	active       *fakeState
	transactions int
}

func (r *fakeRunner) Run(
	ctx context.Context,
	fn func(context.Context, Queries, db.DBTX) error,
) error {
	r.transactions++
	staged := r.state.clone()
	r.active = &staged
	queries := &fakeQueries{runner: r, state: &staged}
	err := fn(ctx, queries, nil)
	r.active = nil
	if err != nil {
		return err
	}
	if r.commitErr != nil {
		return r.commitErr
	}
	r.state = staged
	return nil
}

type fakeQueries struct {
	runner *fakeRunner
	state  *fakeState
}

func (q *fakeQueries) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) {
	return q.state.caseRow, nil
}

func (q *fakeQueries) UpdateCaseStatus(_ context.Context, arg db.UpdateCaseStatusParams) (db.Case, error) {
	q.state.caseRow.Status = arg.Status
	q.state.statusUpdates = append(q.state.statusUpdates, arg.Status)
	return q.state.caseRow, nil
}

func (q *fakeQueries) LockAnalysisVersionSeq(context.Context, pgtype.UUID) error {
	return nil
}

func (q *fakeQueries) MaxAnalysisVersion(context.Context, pgtype.UUID) (int32, error) {
	return q.runner.latest, nil
}

func (q *fakeQueries) CreateAnalysis(_ context.Context, arg db.CreateAnalysisParams) (db.AiAnalysis, error) {
	analysis := db.AiAnalysis{
		ID: arg.ID, CaseID: arg.CaseID, Version: arg.Version, Status: arg.Status,
		ModelName: arg.ModelName, PromptVersion: arg.PromptVersion,
	}
	q.state.analyses = append(q.state.analyses, analysis)
	return analysis, nil
}

func (q *fakeQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	audit := db.AuditEvent{
		ID: arg.ID, ScopeType: "CASE", CaseID: arg.CaseID, EventType: arg.EventType,
		ActorID: arg.ActorID, ActorRole: arg.ActorRole, AnalysisID: arg.AnalysisID,
		Metadata: append([]byte(nil), arg.Metadata...),
	}
	q.state.audits = append(q.state.audits, audit)
	return audit, nil
}

func (q *fakeQueries) CreateOutboxEvent(_ context.Context, arg db.CreateOutboxEventParams) (db.OutboxEvent, error) {
	if q.runner.outboxErr != nil {
		return db.OutboxEvent{}, q.runner.outboxErr
	}
	outbox := db.OutboxEvent{
		ID: arg.ID, CaseID: arg.CaseID, AnalysisID: arg.AnalysisID,
		EventType: arg.EventType, Payload: append([]byte(nil), arg.Payload...), Status: "PENDING",
	}
	q.state.outboxes = append(q.state.outboxes, outbox)
	return outbox, nil
}

func TestQuotaAvailableCommitsActionAnalysisAuditAndOutbox(t *testing.T) {
	runner := newFakeRunner(workflow.StateChecking, 1)
	service := &Service{runner: runner, maxReanalysis: 3}
	request := testRequest(workflow.EventCheckerRejected)

	result, err := service.Run(context.Background(), request, persistFakeAction(runner))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Queued || result.Case.Status != string(workflow.StateAIAnalysis) {
		t.Fatalf("result = %+v, want QUEUED in AI_ANALYSIS", result)
	}
	if runner.state.businessActions != 1 {
		t.Fatalf("business actions = %d, want 1", runner.state.businessActions)
	}
	if !reflect.DeepEqual(runner.state.statusUpdates, []string{"AI_ANALYSIS"}) {
		t.Fatalf("status updates = %v, want [AI_ANALYSIS]", runner.state.statusUpdates)
	}
	if len(runner.state.analyses) != 1 {
		t.Fatalf("analyses = %d, want 1", len(runner.state.analyses))
	}
	analysis := runner.state.analyses[0]
	if analysis.Version != 2 || analysis.Status != "GENERATING" || analysis.WorkerAttemptID.Valid || analysis.WorkerStartedAt.Valid {
		t.Fatalf("analysis = %+v, want unclaimed GENERATING version 2", analysis)
	}
	if result.Analysis == nil || result.Analysis.ID != analysis.ID {
		t.Fatalf("result analysis = %+v, want %v", result.Analysis, analysis.ID)
	}
	if len(runner.state.audits) != 1 || runner.state.audits[0].EventType != "AI_ANALYSIS_STARTED" {
		t.Fatalf("audits = %+v, want AI_ANALYSIS_STARTED", runner.state.audits)
	}
	if runner.state.audits[0].AnalysisID != analysis.ID || string(runner.state.audits[0].Metadata) != `{"version":2}` {
		t.Fatalf("analysis audit = %+v", runner.state.audits[0])
	}
	if len(runner.state.outboxes) != 1 {
		t.Fatalf("outboxes = %d, want 1", len(runner.state.outboxes))
	}
	outbox := runner.state.outboxes[0]
	if outbox.Status != "PENDING" || outbox.EventType != "AI_ANALYSIS_REQUESTED" || outbox.AnalysisID != analysis.ID {
		t.Fatalf("outbox = %+v", outbox)
	}
	var payload struct {
		CaseID     string `json:"case_id"`
		AnalysisID string `json:"analysis_id"`
		Version    int32  `json:"version"`
	}
	if err := json.Unmarshal(outbox.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CaseID != request.CaseID.String() || payload.AnalysisID != analysis.ID.String() || payload.Version != 2 {
		t.Fatalf("outbox payload = %+v", payload)
	}
}

func TestQuotaExhaustedCommitsActionAndEscalatesWithoutNewJob(t *testing.T) {
	runner := newFakeRunner(workflow.StateSigning, 4)
	service := &Service{runner: runner, maxReanalysis: 3}

	result, err := service.Run(context.Background(), testRequest(workflow.EventSignerRejected), persistFakeAction(runner))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LimitReached || result.Case.Status != string(workflow.StateEscalationRequired) {
		t.Fatalf("result = %+v, want LIMIT_REACHED in ESCALATION_REQUIRED", result)
	}
	if runner.state.businessActions != 1 {
		t.Fatalf("business actions = %d, want 1", runner.state.businessActions)
	}
	if !reflect.DeepEqual(runner.state.statusUpdates, []string{"AI_ANALYSIS", "ESCALATION_REQUIRED"}) {
		t.Fatalf("status updates = %v", runner.state.statusUpdates)
	}
	if len(runner.state.analyses) != 0 || len(runner.state.outboxes) != 0 {
		t.Fatalf("new rows = analyses %d outboxes %d, want zero", len(runner.state.analyses), len(runner.state.outboxes))
	}
	if result.Analysis != nil || result.Outbox != nil {
		t.Fatalf("limit result unexpectedly contains job: %+v", result)
	}
	if len(runner.state.audits) != 1 || runner.state.audits[0].EventType != "REANALYSIS_LIMIT_REACHED" {
		t.Fatalf("audits = %+v", runner.state.audits)
	}
	if runner.state.audits[0].AnalysisID != runner.state.caseRow.CurrentAnalysisID {
		t.Fatalf("limit audit analysis = %v, want current %v", runner.state.audits[0].AnalysisID, runner.state.caseRow.CurrentAnalysisID)
	}
	if string(runner.state.audits[0].Metadata) != `{"latest_analysis_version":4,"max_reanalysis":3}` {
		t.Fatalf("limit metadata = %s", runner.state.audits[0].Metadata)
	}
}

func TestOutboxFailureRollsBackEntireTransaction(t *testing.T) {
	runner := newFakeRunner(workflow.StateExecution, 1)
	runner.outboxErr = errors.New("outbox unavailable")
	service := &Service{runner: runner, maxReanalysis: 3}

	_, err := service.Run(context.Background(), testRequest(workflow.EventExecutionFailed), persistFakeAction(runner))
	if err == nil || !errors.Is(err, runner.outboxErr) {
		t.Fatalf("error = %v, want outbox error", err)
	}
	if runner.state.businessActions != 0 {
		t.Fatalf("business actions = %d after rollback, want 0", runner.state.businessActions)
	}
	if runner.state.caseRow.Status != string(workflow.StateExecution) || len(runner.state.statusUpdates) != 0 {
		t.Fatalf("case mutated after rollback: %+v", runner.state)
	}
	if len(runner.state.analyses) != 0 || len(runner.state.audits) != 0 || len(runner.state.outboxes) != 0 {
		t.Fatalf("rows survived rollback: analyses=%d audits=%d outboxes=%d", len(runner.state.analyses), len(runner.state.audits), len(runner.state.outboxes))
	}
}

func TestRerunCannotCreateDuplicateAnalysisOrOutbox(t *testing.T) {
	runner := newFakeRunner(workflow.StateChecking, 1)
	service := &Service{runner: runner, maxReanalysis: 3}
	request := testRequest(workflow.EventCheckerRejected)
	if _, err := service.Run(context.Background(), request, persistFakeAction(runner)); err != nil {
		t.Fatal(err)
	}
	runner.latest = 2
	_, err := service.Run(context.Background(), request, persistFakeAction(runner))
	if !workflow.IsInvalidTransition(err) {
		t.Fatalf("rerun error = %v, want invalid transition", err)
	}
	if runner.state.businessActions != 1 || len(runner.state.analyses) != 1 || len(runner.state.outboxes) != 1 {
		t.Fatalf("rerun committed duplicates: %+v", runner.state)
	}
}

func newFakeRunner(state workflow.State, latest int32) *fakeRunner {
	return &fakeRunner{
		latest: latest,
		state: fakeState{caseRow: db.Case{
			ID: testUUID(1), Status: string(state), CurrentAnalysisID: testUUID(byte(latest + 10)),
		}},
	}
}

func testRequest(trigger workflow.Event) Request {
	return Request{
		CaseID: testUUID(1), Trigger: trigger, ActorID: testUUID(2), ActorRole: actorRole(trigger),
		ModelName: "model", PromptVersion: "prompt-v1",
	}
}

func persistFakeAction(runner *fakeRunner) PersistAction {
	return func(context.Context, db.DBTX, db.Case) error {
		runner.active.businessActions++
		return nil
	}
}

func actorRole(trigger workflow.Event) string {
	switch trigger {
	case workflow.EventCheckerRejected:
		return "CHECKER"
	case workflow.EventSignerRejected:
		return "SIGNER"
	default:
		return "EXECUTER"
	}
}

func testUUID(last byte) pgtype.UUID {
	var id [16]byte
	id[15] = last
	return pgtype.UUID{Bytes: id, Valid: true}
}
