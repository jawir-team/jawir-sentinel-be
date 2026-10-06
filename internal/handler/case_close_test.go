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

type fakeCloseTxQueries struct {
	caseResult   db.Case
	caseErr      error
	participants []db.CaseParticipant
	listErr      error

	generating    bool
	generatingErr error
	running       bool
	runningErr    error

	closeArg   db.CloseCaseParams
	closeErr   error
	closeCalls int

	auditArg   db.AppendCaseAuditEventParams
	auditErr   error
	auditCalls int
}

var _ handler.CloseTxQueries = (*fakeCloseTxQueries)(nil)

func (f *fakeCloseTxQueries) GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, f.caseErr
}

func (f *fakeCloseTxQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, f.listErr
}

func (f *fakeCloseTxQueries) ExistsGeneratingAnalysis(context.Context, pgtype.UUID) (bool, error) {
	return f.generating, f.generatingErr
}

func (f *fakeCloseTxQueries) ExistsRunningExecution(context.Context, pgtype.UUID) (bool, error) {
	return f.running, f.runningErr
}

func (f *fakeCloseTxQueries) CloseCase(_ context.Context, arg db.CloseCaseParams) (db.Case, error) {
	f.closeCalls++
	f.closeArg = arg
	closed := f.caseResult
	closed.Status = "CLOSED"
	closed.ClosedBy = arg.ClosedBy
	closed.CloseReason = pgtype.Text{String: arg.CloseReason, Valid: true}
	return closed, f.closeErr
}

func (f *fakeCloseTxQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, f.auditErr
}

type fakeCloseStore struct {
	queries *fakeCloseTxQueries
	calls   int
	txErr   error
}

var _ handler.CloseCaseStore = (*fakeCloseStore)(nil)

func (f *fakeCloseStore) RunCloseTx(ctx context.Context, fn func(context.Context, handler.CloseTxQueries) error) error {
	f.calls++
	if err := fn(ctx, f.queries); err != nil {
		return err
	}
	return f.txErr
}

func closeTestQueries(actor auth.User) *fakeCloseTxQueries {
	caseID := handlerTestUUID(4)
	return &fakeCloseTxQueries{
		caseResult: db.Case{
			ID:         caseID,
			CaseNumber: "CASE-X",
			CaseTypeID: handlerTestUUID(3),
			Title:      "Leaking pipe",
			Urgency:    "MEDIUM",
			Status:     "CHECKING",
			CreatedBy:  actor.ID,
			OwnerID:    actor.ID,
			CreatedAt:  caseTestTime,
			UpdatedAt:  caseTestTime,
		},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: "CHECKER", Required: true, Status: "ACTIVE"},
		},
	}
}

func serveCloseCase(actor auth.User, queries *fakeCloseTxQueries, body string) *httptest.ResponseRecorder {
	store := &fakeCloseStore{queries: queries}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/close",
		handlerTestUUID(4).String(),
		body,
		&actor,
	)
	response := httptest.NewRecorder()
	handler.CloseCase(store).ServeHTTP(response, request)
	return response
}

func TestCloseCaseGeneratingAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)
	queries.generating = true

	response := serveCloseCase(actor, queries, `{"reason":"No longer needed"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	if queries.closeCalls != 0 || queries.auditCalls != 0 {
		t.Fatalf("side-effect calls = close %d, audit %d; want all zero", queries.closeCalls, queries.auditCalls)
	}
}

func TestCloseCaseRunningExecution(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)
	queries.running = true

	response := serveCloseCase(actor, queries, `{"reason":"No longer needed"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	if queries.closeCalls != 0 {
		t.Fatalf("CloseCase calls = %d, want 0", queries.closeCalls)
	}
}

func TestCloseCaseAlreadyDone(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)
	queries.caseResult.Status = "DONE"

	response := serveCloseCase(actor, queries, `{"reason":"Completed elsewhere"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	if queries.closeCalls != 0 {
		t.Fatalf("CloseCase calls = %d, want 0", queries.closeCalls)
	}
}

func TestCloseCaseNonParticipant(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)
	queries.participants = nil

	response := serveCloseCase(actor, queries, `{"reason":"No longer needed"}`)

	assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
}

func TestCloseCaseEmptyReason(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)

	response := serveCloseCase(actor, queries, `{"reason":"  "}`)

	assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
	if queries.closeCalls != 0 {
		t.Fatalf("CloseCase calls = %d, want 0", queries.closeCalls)
	}
}

func TestCloseCaseSuccess(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := closeTestQueries(actor)

	response := serveCloseCase(actor, queries, `{"reason":"  No longer needed  "}`)

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
	if body.Data.Status != "CLOSED" {
		t.Errorf("response status = %q, want CLOSED", body.Data.Status)
	}
	if queries.closeCalls != 1 {
		t.Fatalf("CloseCase calls = %d, want 1", queries.closeCalls)
	}
	if queries.closeArg.ID != queries.caseResult.ID || queries.closeArg.ClosedBy != actor.ID || queries.closeArg.CloseReason != "No longer needed" {
		t.Errorf("CloseCase arg = %+v, want case ID, actor ID, and trimmed reason", queries.closeArg)
	}
	if queries.auditCalls != 1 {
		t.Fatalf("AppendCaseAuditEvent calls = %d, want 1", queries.auditCalls)
	}
	if queries.auditArg.EventType != "CASE_CLOSED" || queries.auditArg.ActorID != actor.ID {
		t.Errorf("audit arg = %+v, want CASE_CLOSED by actor", queries.auditArg)
	}
	if !queries.auditArg.ActorRole.Valid || queries.auditArg.ActorRole.String != "CHECKER" {
		t.Errorf("audit actor role = %+v, want CHECKER", queries.auditArg.ActorRole)
	}
}
