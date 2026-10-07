package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

type fakeCheckerDecisionQueries struct {
	caseResult   db.Case
	caseErr      error
	participants []db.CaseParticipant
	decisions    []db.Decision
	evidences    []db.CaseEvidence

	createArg   db.CreateDecisionParams
	createErr   error
	createCalls int
	auditArg    db.AppendCaseAuditEventParams
	auditCalls  int
	updateArg   db.UpdateCaseStatusParams
	updateCalls int
}

var (
	_ handler.CheckerDecisionTxQueries = (*fakeCheckerDecisionQueries)(nil)
	_ db.DBTX                          = (*fakeCheckerDecisionQueries)(nil)
)

func (f *fakeCheckerDecisionQueries) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, f.caseErr
}

func (f *fakeCheckerDecisionQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, nil
}

func (f *fakeCheckerDecisionQueries) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) {
	return f.decisions, nil
}

func (f *fakeCheckerDecisionQueries) ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error) {
	return f.evidences, nil
}

func (f *fakeCheckerDecisionQueries) CreateDecision(_ context.Context, arg db.CreateDecisionParams) (db.Decision, error) {
	f.createCalls++
	f.createArg = arg
	if f.createErr != nil {
		return db.Decision{}, f.createErr
	}
	return db.Decision{
		ID: arg.ID, CaseID: arg.CaseID, AnalysisID: arg.AnalysisID,
		ActorID: arg.ActorID, ActorRole: arg.ActorRole, Decision: arg.Decision,
		Reason: arg.Reason, Comment: arg.Comment,
	}, nil
}

func (f *fakeCheckerDecisionQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, nil
}

func (f *fakeCheckerDecisionQueries) UpdateCaseStatus(_ context.Context, arg db.UpdateCaseStatusParams) (db.Case, error) {
	f.updateCalls++
	f.updateArg = arg
	updated := f.caseResult
	updated.Status = arg.Status
	return updated, nil
}

// The fake orchestrator passes this value as db.DBTX. The handler recognizes
// that it also implements CheckerDecisionTxQueries, so these SQL-level methods
// are deliberately never called.
func (f *fakeCheckerDecisionQueries) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected SQL Exec")
}

func (f *fakeCheckerDecisionQueries) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected SQL Query")
}

func (f *fakeCheckerDecisionQueries) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected SQL QueryRow")
}

type fakeCheckerDecisionStore struct {
	queries *fakeCheckerDecisionQueries
	calls   int
}

var _ handler.CheckerDecisionStore = (*fakeCheckerDecisionStore)(nil)

func (f *fakeCheckerDecisionStore) RunCheckerDecisionTx(ctx context.Context, fn func(context.Context, handler.CheckerDecisionTxQueries) error) error {
	f.calls++
	return fn(ctx, f.queries)
}

type fakeCheckerReanalysis struct {
	queries *fakeCheckerDecisionQueries
	outcome reanalysis.Outcome
	calls   int
	request reanalysis.Request
}

var _ handler.ReanalysisOrchestrator = (*fakeCheckerReanalysis)(nil)

func (f *fakeCheckerReanalysis) Run(ctx context.Context, request reanalysis.Request, persist reanalysis.PersistAction) (reanalysis.Result, error) {
	f.calls++
	f.request = request
	if err := persist(ctx, f.queries, f.queries.caseResult); err != nil {
		return reanalysis.Result{}, fmt.Errorf("persist reanalysis trigger: %w", err)
	}
	resultCase := f.queries.caseResult
	switch f.outcome {
	case reanalysis.Queued:
		resultCase.Status = string(workflow.StateAIAnalysis)
	case reanalysis.LimitReached:
		resultCase.Status = string(workflow.StateEscalationRequired)
	default:
		return reanalysis.Result{}, fmt.Errorf("unexpected fake outcome %q", f.outcome)
	}
	return reanalysis.Result{Outcome: f.outcome, Case: resultCase}, nil
}

func checkerDecisionFixture(actor auth.User) *fakeCheckerDecisionQueries {
	caseID := handlerTestUUID(4)
	analysisID := handlerTestUUID(5)
	return &fakeCheckerDecisionQueries{
		caseResult: db.Case{
			ID: caseID, CaseNumber: "CASE-X", CaseTypeID: handlerTestUUID(3),
			Title: "Leaking pipe", Urgency: "MEDIUM", Status: string(workflow.StateChecking),
			CreatedBy: actor.ID, OwnerID: actor.ID, CurrentAnalysisID: analysisID,
			CreatedAt: caseTestTime, UpdatedAt: caseTestTime,
		},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: "CHECKER", Required: true, Status: "ACTIVE"},
		},
		evidences: []db.CaseEvidence{{ID: handlerTestUUID(6), CaseID: caseID}},
	}
}

func serveCheckerDecision(actor auth.User, queries *fakeCheckerDecisionQueries, reanalysisOutcome reanalysis.Outcome, body string) (*httptest.ResponseRecorder, *fakeCheckerDecisionStore, *fakeCheckerReanalysis) {
	store := &fakeCheckerDecisionStore{queries: queries}
	orchestrator := &fakeCheckerReanalysis{queries: queries, outcome: reanalysisOutcome}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/checker-decisions",
		handlerTestUUID(4).String(), body, &actor,
	)
	response := httptest.NewRecorder()
	handler.RecordCheckerDecision(handler.CheckerDecider{Store: store, Reanalysis: orchestrator}).ServeHTTP(response, request)
	return response, store, orchestrator
}

func assertCheckerDecisionSuccess(t *testing.T, response *httptest.ResponseRecorder, decision, status string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			DecisionID string `json:"decision_id"`
			Decision   string `json:"decision"`
			CaseStatus string `json:"case_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.DecisionID == "" || body.Data.Decision != decision || body.Data.CaseStatus != status {
		t.Errorf("response data = %+v, want decision=%s status=%s and a decision ID", body.Data, decision, status)
	}
}

func TestRecordCheckerDecisionApproveKeepsCheckingUntilAllRequiredApprove(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: handlerTestUUID(8),
		Role: "CHECKER", Required: true, Status: "ACTIVE",
	})

	response, store, orchestrator := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":" approve ","comment":" Looks good "}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "CHECKING")
	if store.calls != 1 || orchestrator.calls != 0 || queries.updateCalls != 0 {
		t.Errorf("calls = store %d, reanalysis %d, update %d; want 1, 0, 0", store.calls, orchestrator.calls, queries.updateCalls)
	}
	if queries.createArg.Reason.Valid || !queries.createArg.Comment.Valid || queries.createArg.Comment.String != "Looks good" {
		t.Errorf("decision optional fields = reason %+v comment %+v", queries.createArg.Reason, queries.createArg.Comment)
	}
	if queries.auditArg.EventType != "CHECKER_APPROVED" || queries.auditArg.AnalysisID != queries.caseResult.CurrentAnalysisID {
		t.Errorf("audit = %+v, want CHECKER_APPROVED for current analysis", queries.auditArg)
	}
}

func TestRecordCheckerDecisionApproveCompletesRequiredCheckers(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	secondCheckerID := handlerTestUUID(8)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: secondCheckerID,
		Role: "CHECKER", Required: true, Status: "ACTIVE",
	})
	queries.decisions = []db.Decision{{
		AnalysisID: queries.caseResult.CurrentAnalysisID,
		ActorID:    secondCheckerID, ActorRole: "CHECKER", Decision: "APPROVE",
	}}

	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "SIGNING")
	if queries.updateCalls != 1 || queries.updateArg.Status != "SIGNING" {
		t.Errorf("status update = calls %d arg %+v, want one SIGNING update", queries.updateCalls, queries.updateArg)
	}
}

func TestRecordCheckerDecisionOptionalCheckerPendingDoesNotBlock(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: handlerTestUUID(8),
		Role: "CHECKER", Required: false, Status: "ACTIVE",
	})

	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "SIGNING")
	if queries.updateCalls != 1 || queries.updateArg.Status != "SIGNING" {
		t.Errorf("status update = calls %d arg %+v, want one SIGNING update", queries.updateCalls, queries.updateArg)
	}
}

func TestRecordCheckerDecisionOldAnalysisApprovalDoesNotCompleteRound(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	secondCheckerID := handlerTestUUID(8)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: secondCheckerID,
		Role: "CHECKER", Required: true, Status: "ACTIVE",
	})
	queries.decisions = []db.Decision{{
		AnalysisID: handlerTestUUID(7), ActorID: secondCheckerID,
		ActorRole: "CHECKER", Decision: "APPROVE",
	}}

	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "CHECKING")
	if queries.updateCalls != 0 {
		t.Errorf("status update calls = %d, want 0", queries.updateCalls)
	}
}

func TestRecordCheckerDecisionLateApprovalRejectedRoundDoesNotTransition(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	secondCheckerID := handlerTestUUID(8)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: secondCheckerID,
		Role: "CHECKER", Required: false, Status: "ACTIVE",
	})
	queries.decisions = []db.Decision{{
		AnalysisID: queries.caseResult.CurrentAnalysisID, ActorID: secondCheckerID,
		ActorRole: "CHECKER", Decision: "REJECT",
	}}

	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	if queries.createCalls != 0 || queries.auditCalls != 0 || queries.updateCalls != 0 {
		t.Errorf("mutation calls = create %d audit %d update %d, want all zero", queries.createCalls, queries.auditCalls, queries.updateCalls)
	}
}

func TestRecordCheckerDecisionRejectQueuesReanalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)

	response, store, orchestrator := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":" Missing SOP ","evidence_ids":["00000000-0000-0000-0000-000000000006"]}`)

	assertCheckerDecisionSuccess(t, response, "REJECT", "AI_ANALYSIS")
	if store.calls != 0 || orchestrator.calls != 1 {
		t.Errorf("calls = store %d reanalysis %d, want 0 and 1", store.calls, orchestrator.calls)
	}
	if orchestrator.request.Trigger != workflow.EventCheckerRejected || orchestrator.request.ActorRole != "CHECKER" || orchestrator.request.ModelName != "" || orchestrator.request.PromptVersion != "" {
		t.Errorf("reanalysis request = %+v", orchestrator.request)
	}
	if queries.createCalls != 1 || !queries.createArg.Reason.Valid || queries.createArg.Reason.String != "Missing SOP" {
		t.Errorf("persisted decision = calls %d arg %+v", queries.createCalls, queries.createArg)
	}
	if queries.auditArg.EventType != "CHECKER_REJECTED" {
		t.Errorf("audit event = %q, want CHECKER_REJECTED", queries.auditArg.EventType)
	}
}

func TestRecordCheckerDecisionRejectLimitReachedIsSuccess(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)

	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.LimitReached,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":"No"}`)

	assertCheckerDecisionSuccess(t, response, "REJECT", "ESCALATION_REQUIRED")
}

func TestRecordCheckerDecisionRejectRequiresReason(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	response, store, orchestrator := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":"  "}`)

	assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
	if store.calls != 0 || orchestrator.calls != 0 {
		t.Error("validation failure must occur before a transaction")
	}
}

func TestRecordCheckerDecisionUnassignedActorIsNotFound(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	queries.participants = nil
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
}

func TestRecordCheckerDecisionWrongState(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	queries.caseResult.Status = "SIGNING"
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
}

func TestRecordCheckerDecisionDuplicate(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	queries.decisions = []db.Decision{{AnalysisID: queries.caseResult.CurrentAnalysisID, ActorID: actor.ID, ActorRole: "CHECKER", Decision: "APPROVE"}}
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
}

func TestRecordCheckerDecisionStaleAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000007","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeStaleAnalysis)
}

func TestRecordCheckerDecisionInvalidDecision(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"MAYBE"}`)

	assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
}

func TestRecordCheckerDecisionRejectsNextState(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := checkerDecisionFixture(actor)
	response, _, _ := serveCheckerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE","next_state":"DONE"}`)

	assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
}
