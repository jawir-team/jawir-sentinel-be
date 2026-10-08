package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

type fakeExecutionQueries struct {
	caseResult      db.Case
	participants    []db.CaseParticipant
	analysisResult  db.AiAnalysis
	decisions       []db.Decision
	exists          bool
	executionResult db.Execution

	existsArg       db.ExistsExecutionForCaseAnalysisParams
	createArg       db.CreateExecutionParams
	createCalls     int
	updateArg       db.UpdateExecutionParams
	updateCalls     int
	updateCaseArg   db.UpdateCaseStatusParams
	updateCaseCalls int
	auditArg        db.AppendCaseAuditEventParams
	auditArgs       []db.AppendCaseAuditEventParams
	auditCalls      int
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

func (f *fakeExecutionQueries) GetExecutionForUpdate(context.Context, pgtype.UUID) (db.Execution, error) {
	return f.executionResult, nil
}

func (f *fakeExecutionQueries) UpdateExecution(_ context.Context, arg db.UpdateExecutionParams) (db.Execution, error) {
	f.updateCalls++
	f.updateArg = arg
	updated := f.executionResult
	updated.Status = arg.Status
	updated.ActionTaken = arg.ActionTaken
	updated.Result = arg.Result
	updated.Blocker = arg.Blocker
	updated.CompletedAt = arg.CompletedAt
	return updated, nil
}

func (f *fakeExecutionQueries) UpdateCaseStatus(_ context.Context, arg db.UpdateCaseStatusParams) (db.Case, error) {
	f.updateCaseCalls++
	f.updateCaseArg = arg
	updated := f.caseResult
	updated.Status = arg.Status
	return updated, nil
}

func (f *fakeExecutionQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	f.auditArgs = append(f.auditArgs, arg)
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
	queries := &fakeExecutionQueries{
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
	queries.executionResult = db.Execution{
		ID: handlerTestUUID(12), CaseID: caseID, AnalysisID: analysisID,
		ExecuterID: actor.ID, Status: "IN_PROGRESS", StartedAt: caseTestTime, CreatedAt: caseTestTime,
	}
	return queries
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

func serveFinalizeExecutionSuccess(actor auth.User, queries *fakeExecutionQueries, body string) (*httptest.ResponseRecorder, *fakeExecutionStore) {
	store := &fakeExecutionStore{queries: queries}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/executions/00000000-0000-0000-0000-00000000000c/success",
		handlerTestUUID(4).String(),
		body,
		&actor,
	)
	chi.RouteContext(request.Context()).URLParams.Add("execution_id", queries.executionResult.ID.String())
	response := httptest.NewRecorder()
	handler.FinalizeExecutionSuccess(store).ServeHTTP(response, request)
	return response, store
}

func TestFinalizeExecutionSuccessValid(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)

	response, store := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"  action completed  ","result":"  worked  "}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
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
	if body.Data.ID != queries.executionResult.ID.String() || body.Data.Status != "SUCCESS" || body.Data.CaseStatus != "DONE" {
		t.Errorf("response data = %+v", body.Data)
	}
	if store.calls != 1 {
		t.Errorf("transaction calls = %d, want 1", store.calls)
	}
	if queries.updateCalls != 1 {
		t.Fatalf("execution update calls = %d, want 1", queries.updateCalls)
	}
	if queries.updateArg.Status != "SUCCESS" || !queries.updateArg.ActionTaken.Valid || queries.updateArg.ActionTaken.String != "action completed" || !queries.updateArg.Result.Valid || queries.updateArg.Result.String != "worked" || queries.updateArg.Blocker.Valid || !queries.updateArg.CompletedAt.Valid {
		t.Errorf("execution update arg = %+v", queries.updateArg)
	}
	if queries.updateCaseCalls != 1 || queries.updateCaseArg.Status != "DONE" {
		t.Errorf("case update = calls %d arg %+v", queries.updateCaseCalls, queries.updateCaseArg)
	}
	if len(queries.auditArgs) != 2 || queries.auditArgs[0].EventType != "EXECUTION_SUCCESS" || queries.auditArgs[1].EventType != "CASE_DONE" {
		t.Fatalf("audits = %+v, want EXECUTION_SUCCESS then CASE_DONE", queries.auditArgs)
	}
	for _, audit := range queries.auditArgs {
		if audit.ActorID != actor.ID || !audit.ActorRole.Valid || audit.ActorRole.String != "EXECUTER" {
			t.Errorf("audit actor = %+v, want executer %s", audit, actor.ID.String())
		}
	}
	if queries.auditArgs[0].AnalysisID != queries.executionResult.AnalysisID {
		t.Errorf("execution audit analysis = %s, want %s", queries.auditArgs[0].AnalysisID.String(), queries.executionResult.AnalysisID.String())
	}
	var executionMetadata struct {
		ExecutionID string `json:"execution_id"`
		ActionTaken string `json:"action_taken"`
	}
	if err := json.Unmarshal(queries.auditArgs[0].Metadata, &executionMetadata); err != nil {
		t.Fatalf("decode execution audit metadata: %v", err)
	}
	if executionMetadata.ExecutionID != queries.executionResult.ID.String() || executionMetadata.ActionTaken != "action completed" {
		t.Errorf("execution audit metadata = %+v", executionMetadata)
	}
	var doneMetadata struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(queries.auditArgs[1].Metadata, &doneMetadata); err != nil {
		t.Fatalf("decode case audit metadata: %v", err)
	}
	if doneMetadata.ExecutionID != queries.executionResult.ID.String() {
		t.Errorf("case audit execution ID = %q", doneMetadata.ExecutionID)
	}
}

func TestFinalizeExecutionSuccessMissingActionTaken(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"  ","result":"worked"}`)

	assertExecutionError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessMissingResult(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"  "}`)

	assertExecutionError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessWrongExecuter(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.executionResult.ExecuterID = handlerTestUUID(13)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"worked"}`)

	assertExecutionError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessWrongCaseState(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.caseResult.Status = string(workflow.StateSigning)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"worked"}`)

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessAlreadyFinalized(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.executionResult.Status = "SUCCESS"

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"worked"}`)

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessStaleAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.executionResult.AnalysisID = handlerTestUUID(6)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"worked"}`)

	assertExecutionError(t, response, http.StatusConflict, httpapi.CodeStaleAnalysis)
	assertNoExecutionFinalized(t, queries)
}

func TestFinalizeExecutionSuccessDifferentCase(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := executionFixture(actor)
	queries.executionResult.CaseID = handlerTestUUID(14)

	response, _ := serveFinalizeExecutionSuccess(actor, queries, `{"action_taken":"action completed","result":"worked"}`)

	assertExecutionError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
	assertNoExecutionFinalized(t, queries)
}

func assertNoExecutionFinalized(t *testing.T, queries *fakeExecutionQueries) {
	t.Helper()
	if queries.updateCalls != 0 || queries.updateCaseCalls != 0 || queries.auditCalls != 0 {
		t.Fatalf("side effects = execution updates %d case updates %d audits %d; want zero", queries.updateCalls, queries.updateCaseCalls, queries.auditCalls)
	}
}
