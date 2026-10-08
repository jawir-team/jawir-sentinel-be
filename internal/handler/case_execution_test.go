package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

type fakeExecutionQueries struct {
	caseResult     db.Case
	participants   []db.CaseParticipant
	analysisResult db.AiAnalysis
	decisions      []db.Decision
	exists         bool

	existsArg   db.ExistsExecutionForCaseAnalysisParams
	createArg   db.CreateExecutionParams
	createCalls int
	auditArg    db.AppendCaseAuditEventParams
	auditCalls  int
}

var _ handler.ExecutionTxQueries = (*fakeExecutionQueries)(nil)

func (f *fakeExecutionQueries) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, nil
}

func (f *fakeExecutionQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, nil
}

func (f *fakeExecutionQueries) GetAnalysis(context.Context, pgtype.UUID) (db.AiAnalysis, error) {
	return f.analysisResult, nil
}

func (f *fakeExecutionQueries) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) {
	return f.decisions, nil
}

func (f *fakeExecutionQueries) ExistsExecutionForCaseAnalysis(_ context.Context, arg db.ExistsExecutionForCaseAnalysisParams) (bool, error) {
	f.existsArg = arg
	return f.exists, nil
}

func (f *fakeExecutionQueries) CreateExecution(_ context.Context, arg db.CreateExecutionParams) (db.Execution, error) {
	f.createCalls++
	f.createArg = arg
	return db.Execution{
		ID: arg.ID, CaseID: arg.CaseID, AnalysisID: arg.AnalysisID,
		ExecuterID: arg.ExecuterID, Status: arg.Status, StartedAt: caseTestTime, CreatedAt: caseTestTime,
	}, nil
}

func (f *fakeExecutionQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, nil
}

type fakeExecutionStore struct {
	queries *fakeExecutionQueries
	calls   int
}

var _ handler.ExecutionStore = (*fakeExecutionStore)(nil)

func (f *fakeExecutionStore) RunExecutionTx(ctx context.Context, fn func(context.Context, handler.ExecutionTxQueries) error) error {
	f.calls++
	return fn(ctx, f.queries)
}

func executionFixture(actor auth.User) *fakeExecutionQueries {
	caseID := handlerTestUUID(4)
	analysisID := handlerTestUUID(5)
	signerID := handlerTestUUID(8)
	return &fakeExecutionQueries{
		caseResult: db.Case{
			ID: caseID, OwnerID: handlerTestUUID(7), Status: string(workflow.StateExecution),
			CurrentAnalysisID: analysisID,
		},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: "EXECUTER", Status: "ACTIVE"},
		},
		analysisResult: db.AiAnalysis{ID: analysisID, CaseID: caseID, Status: "COMPLETED"},
		decisions: []db.Decision{
			{ID: handlerTestUUID(11), CaseID: caseID, AnalysisID: analysisID, ActorID: signerID, ActorRole: "SIGNER", Decision: "APPROVE"},
		},
	}
}

func serveStartExecution(actor auth.User, queries *fakeExecutionQueries, analysisID string) (*httptest.ResponseRecorder, *fakeExecutionStore) {
	store := &fakeExecutionStore{queries: queries}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/executions",
		handlerTestUUID(4).String(),
		`{"analysis_id":"`+analysisID+`"}`,
		&actor,
	)
	response := httptest.NewRecorder()
	handler.StartExecution(store).ServeHTTP(response, request)
	return response, store
}

func assertExecutionError(t *testing.T, response *httptest.ResponseRecorder, status int, code httpapi.ErrorCode) {
	t.Helper()
	assertCaseTypeAPIError(t, response, status, code)
}

func TestStartExecutionValid(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)

	response, store := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			CaseStatus string `json:"case_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID == "" || body.Data.Status != "IN_PROGRESS" || body.Data.CaseStatus != "EXECUTION" {
		t.Errorf("response data = %+v", body.Data)
	}
	if store.calls != 1 {
		t.Errorf("transaction calls = %d, want 1", store.calls)
	}
	if queries.createCalls != 1 || queries.createArg.Status != "IN_PROGRESS" || queries.createArg.ExecuterID != actor.ID {
		t.Errorf("execution create = calls %d arg %+v", queries.createCalls, queries.createArg)
	}
	if queries.existsArg.CaseID != queries.caseResult.ID || queries.existsArg.AnalysisID != queries.analysisResult.ID {
		t.Errorf("duplicate guard arg = %+v", queries.existsArg)
	}
	if queries.auditCalls != 1 || queries.auditArg.EventType != "EXECUTION_STARTED" || queries.auditArg.ActorID != actor.ID || !queries.auditArg.ActorRole.Valid || queries.auditArg.ActorRole.String != "EXECUTER" || queries.auditArg.AnalysisID != queries.analysisResult.ID {
		t.Errorf("audit = calls %d arg %+v", queries.auditCalls, queries.auditArg)
	}
	var metadata struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(queries.auditArg.Metadata, &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata.ExecutionID == "" || metadata.ExecutionID != queries.createArg.ID.String() {
		t.Errorf("audit execution_id = %q, want %q", metadata.ExecutionID, queries.createArg.ID.String())
	}
}

func TestStartExecutionWrongState(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.caseResult.Status = string(workflow.StateSigning)

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionUnassignedActor(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.participants = nil

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionWrongRole(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.participants[0].Role = "CHECKER"

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionStaleAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)

	response, _ := serveStartExecution(actor, queries, handlerTestUUID(6).String())

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionWithoutSignerApproval(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.decisions = []db.Decision{{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, AnalysisID: queries.analysisResult.ID,
		ActorID: handlerTestUUID(8), ActorRole: "SIGNER", Decision: "REJECT",
	}}

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionDuplicate(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.exists = true

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionCreated(t, queries)
}

func TestStartExecutionMakerIsExecuter(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.caseResult.OwnerID = actor.ID

	response, _ := serveStartExecution(actor, queries, queries.analysisResult.ID.String())

	assertExecutionError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	assertNoExecutionCreated(t, queries)
}

func assertNoExecutionCreated(t *testing.T, queries *fakeExecutionQueries) {
	t.Helper()
	if queries.createCalls != 0 || queries.auditCalls != 0 {
		t.Fatalf("side effects = execution %d audit %d; want zero", queries.createCalls, queries.auditCalls)
	}
}
