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
)

type fakeSubmitTxQueries struct {
	caseResult   db.Case
	caseErr      error
	participants []db.CaseParticipant
	listErr      error

	updateArg   db.UpdateCaseStatusParams
	updateErr   error
	updateCalls int

	analysisArg   db.CreateAnalysisParams
	analysisErr   error
	analysisCalls int

	outboxArg   db.CreateOutboxEventParams
	outboxErr   error
	outboxCalls int

	auditArgs  []db.AppendCaseAuditEventParams
	auditErr   error
	auditCalls int
}

var _ handler.SubmitTxQueries = (*fakeSubmitTxQueries)(nil)

func (f *fakeSubmitTxQueries) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, f.caseErr
}

func (f *fakeSubmitTxQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, f.listErr
}

func (f *fakeSubmitTxQueries) UpdateCaseStatus(_ context.Context, arg db.UpdateCaseStatusParams) (db.Case, error) {
	f.updateCalls++
	f.updateArg = arg
	updated := f.caseResult
	updated.Status = arg.Status
	return updated, f.updateErr
}

func (f *fakeSubmitTxQueries) CreateAnalysis(_ context.Context, arg db.CreateAnalysisParams) (db.AiAnalysis, error) {
	f.analysisCalls++
	f.analysisArg = arg
	return db.AiAnalysis{
		ID:            arg.ID,
		CaseID:        arg.CaseID,
		Version:       arg.Version,
		Status:        arg.Status,
		ModelName:     arg.ModelName,
		PromptVersion: arg.PromptVersion,
	}, f.analysisErr
}

func (f *fakeSubmitTxQueries) CreateOutboxEvent(_ context.Context, arg db.CreateOutboxEventParams) (db.OutboxEvent, error) {
	f.outboxCalls++
	f.outboxArg = arg
	return db.OutboxEvent{
		ID:         arg.ID,
		CaseID:     arg.CaseID,
		AnalysisID: arg.AnalysisID,
		EventType:  arg.EventType,
		Payload:    arg.Payload,
	}, f.outboxErr
}

func (f *fakeSubmitTxQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArgs = append(f.auditArgs, arg)
	return db.AuditEvent{}, f.auditErr
}

type fakeSubmitStore struct {
	queries *fakeSubmitTxQueries
	calls   int
	txErr   error
}

var _ handler.SubmitCaseStore = (*fakeSubmitStore)(nil)

func (f *fakeSubmitStore) RunSubmitTx(ctx context.Context, fn func(context.Context, handler.SubmitTxQueries) error) error {
	f.calls++
	if err := fn(ctx, f.queries); err != nil {
		return err
	}
	return f.txErr
}

func submitTestQueries(actor auth.User) *fakeSubmitTxQueries {
	caseID := handlerTestUUID(4)
	return &fakeSubmitTxQueries{
		caseResult: db.Case{
			ID:          caseID,
			CaseNumber:  "CASE-X",
			CaseTypeID:  handlerTestUUID(3),
			Title:       "Leaking pipe",
			Description: "",
			Urgency:     "MEDIUM",
			Status:      "DRAFT",
			CreatedBy:   actor.ID,
			OwnerID:     actor.ID,
			CreatedAt:   caseTestTime,
			UpdatedAt:   caseTestTime,
		},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: "MAKER", Required: true, Status: "ACTIVE"},
			{ID: handlerTestUUID(11), CaseID: caseID, UserID: handlerTestUUID(8), Role: "CHECKER", Required: true, Status: "ACTIVE"},
			{ID: handlerTestUUID(12), CaseID: caseID, UserID: handlerTestUUID(7), Role: "SIGNER", Required: true, Status: "ACTIVE"},
			{ID: handlerTestUUID(13), CaseID: caseID, UserID: handlerTestUUID(6), Role: "EXECUTER", Required: true, Status: "ACTIVE"},
		},
	}
}

func serveSubmitCase(actor auth.User, queries *fakeSubmitTxQueries) *httptest.ResponseRecorder {
	store := &fakeSubmitStore{queries: queries}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/submit",
		handlerTestUUID(4).String(),
		"",
		&actor,
	)
	response := httptest.NewRecorder()
	handler.SubmitCase(store).ServeHTTP(response, request)
	return response
}

func TestSubmitCaseDuplicateSubmit(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := submitTestQueries(actor)
	queries.caseResult.Status = "SUBMITTED"

	response := serveSubmitCase(actor, queries)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	if queries.analysisCalls != 0 || queries.outboxCalls != 0 || queries.auditCalls != 0 {
		t.Fatalf("side-effect calls = analysis %d, outbox %d, audit %d; want all zero", queries.analysisCalls, queries.outboxCalls, queries.auditCalls)
	}
}

func TestSubmitCaseNonParticipant(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := submitTestQueries(actor)
	queries.participants = queries.participants[1:]

	response := serveSubmitCase(actor, queries)

	assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
}

func TestSubmitCaseMissingSigner(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := submitTestQueries(actor)
	queries.participants = append(queries.participants[:2], queries.participants[3])

	response := serveSubmitCase(actor, queries)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeCardinalityViolation)
}

func TestSubmitCaseSoDViolation(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := submitTestQueries(actor)
	queries.participants[1].UserID = actor.ID

	response := serveSubmitCase(actor, queries)

	assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeSegregationOfDutiesViolation)
}

func TestSubmitCaseSuccess(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := submitTestQueries(actor)

	response := serveSubmitCase(actor, queries)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Status != "AI_ANALYSIS" {
		t.Errorf("response status = %q, want AI_ANALYSIS", body.Data.Status)
	}
	if queries.updateCalls != 1 || queries.updateArg.Status != "AI_ANALYSIS" {
		t.Errorf("UpdateCaseStatus = calls %d arg %+v, want status AI_ANALYSIS", queries.updateCalls, queries.updateArg)
	}
	if queries.analysisCalls != 1 || queries.analysisArg.Version != 1 || queries.analysisArg.Status != "GENERATING" {
		t.Errorf("CreateAnalysis = calls %d arg %+v, want version 1 GENERATING", queries.analysisCalls, queries.analysisArg)
	}
	if queries.auditCalls != 2 {
		t.Fatalf("AppendCaseAuditEvent calls = %d, want 2", queries.auditCalls)
	}
	if got := []string{queries.auditArgs[0].EventType, queries.auditArgs[1].EventType}; got[0] != "CASE_SUBMITTED" || got[1] != "AI_ANALYSIS_STARTED" {
		t.Errorf("audit events = %v, want [CASE_SUBMITTED AI_ANALYSIS_STARTED]", got)
	}
	if queries.auditArgs[1].AnalysisID != queries.analysisArg.ID || !queries.auditArgs[1].AnalysisID.Valid {
		t.Errorf("analysis audit ID = %v, want created analysis ID %v", queries.auditArgs[1].AnalysisID, queries.analysisArg.ID)
	}
	if queries.outboxCalls != 1 || queries.outboxArg.EventType != "AI_ANALYSIS_REQUESTED" {
		t.Fatalf("CreateOutboxEvent = calls %d arg %+v, want AI_ANALYSIS_REQUESTED", queries.outboxCalls, queries.outboxArg)
	}
	if queries.outboxArg.AnalysisID != queries.analysisArg.ID {
		t.Errorf("outbox analysis ID = %v, want %v", queries.outboxArg.AnalysisID, queries.analysisArg.ID)
	}
	var payload struct {
		CaseID     string `json:"case_id"`
		AnalysisID string `json:"analysis_id"`
		Version    int32  `json:"version"`
	}
	if err := json.Unmarshal(queries.outboxArg.Payload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if payload.CaseID != queries.caseResult.ID.String() || payload.AnalysisID != queries.analysisArg.ID.String() || payload.Version != 1 {
		t.Errorf("outbox payload = %+v, want case/analysis IDs and version 1", payload)
	}
}
