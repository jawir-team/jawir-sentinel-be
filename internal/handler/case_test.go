package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeCaseStore struct {
	createResult     db.Case
	createErr        error
	createArg        db.CreateCaseParams
	createCalls      int
	participantArg   db.CreateCaseParticipantParams
	participantCalls int
	auditArg         db.AppendCaseAuditEventParams
	auditCalls       int

	getResult db.Case
	getErr    error
	getCalls  int

	listResult []db.Case
	listErr    error
	listArg    db.ListCasesForUserParams
	listCalls  int

	updateResult db.Case
	updateErr    error
	updateArg    db.UpdateCaseParams
	updateCalls  int

	isParticipant      bool
	isParticipantErr   error
	isParticipantCalls int
}

var _ handler.CaseStore = (*fakeCaseStore)(nil)

func (f *fakeCaseStore) CreateCase(_ context.Context, arg db.CreateCaseParams) (db.Case, error) {
	f.createCalls++
	f.createArg = arg
	return f.createResult, f.createErr
}

func (f *fakeCaseStore) CreateCaseParticipant(_ context.Context, arg db.CreateCaseParticipantParams) (db.CaseParticipant, error) {
	f.participantCalls++
	f.participantArg = arg
	return db.CaseParticipant{}, nil
}

func (f *fakeCaseStore) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, nil
}

func (f *fakeCaseStore) GetCase(_ context.Context, _ pgtype.UUID) (db.Case, error) {
	f.getCalls++
	return f.getResult, f.getErr
}

func (f *fakeCaseStore) ListCasesForUser(_ context.Context, arg db.ListCasesForUserParams) ([]db.Case, error) {
	f.listCalls++
	f.listArg = arg
	return f.listResult, f.listErr
}

func (f *fakeCaseStore) UpdateCase(_ context.Context, arg db.UpdateCaseParams) (db.Case, error) {
	f.updateCalls++
	f.updateArg = arg
	return f.updateResult, f.updateErr
}

func (f *fakeCaseStore) IsActiveCaseParticipant(_ context.Context, _ db.IsActiveCaseParticipantParams) (bool, error) {
	f.isParticipantCalls++
	return f.isParticipant, f.isParticipantErr
}

var caseTestTime = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func caseTestCase(userID, caseTypeID pgtype.UUID) db.Case {
	return db.Case{
		ID:          handlerTestUUID(4),
		CaseNumber:  "CASE-X",
		CaseTypeID:  caseTypeID,
		Title:       "Leaking pipe",
		Description: "",
		Urgency:     "MEDIUM",
		Status:      "DRAFT",
		CreatedBy:   userID,
		OwnerID:     userID,
		CreatedAt:   caseTestTime,
		UpdatedAt:   caseTestTime,
	}
}

func caseJSON(c db.Case) map[string]any {
	userID := "00000000-0000-0000-0000-000000000009"
	return map[string]any{
		"id":                  "00000000-0000-0000-0000-000000000004",
		"case_number":         c.CaseNumber,
		"case_type_id":        "00000000-0000-0000-0000-000000000003",
		"title":               c.Title,
		"description":         c.Description,
		"urgency":             c.Urgency,
		"status":              c.Status,
		"created_by":          userID,
		"owner_id":            userID,
		"current_analysis_id": nil,
		"closed_by":           nil,
		"close_reason":        nil,
		"closed_at":           nil,
		"created_at":          "2026-10-06T12:00:00Z",
		"updated_at":          "2026-10-06T12:00:00Z",
	}
}

func caseTestUser(role string) auth.User {
	return auth.User{ID: handlerTestUUID(9), SystemRole: role}
}

func caseRequestWithID(method, target, id, body string, user *auth.User) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx)
	if user != nil {
		ctx = auth.WithUser(ctx, *user)
	}
	return request.WithContext(ctx)
}

func TestCreateCase(t *testing.T) {
	user := caseTestUser(auth.SystemRoleUser)
	caseTypeID := handlerTestUUID(3)
	created := caseTestCase(user.ID, caseTypeID)

	tests := []struct {
		name       string
		user       *auth.User
		body       string
		wantStatus int
		wantCode   httpapi.ErrorCode
	}{
		{
			name:       "rejects unauthenticated request",
			body:       `{"case_type_id":"00000000-0000-0000-0000-000000000003","title":"x"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   httpapi.CodeUnauthorized,
		},
		{
			name:       "rejects missing title",
			user:       &user,
			body:       `{"case_type_id":"00000000-0000-0000-0000-000000000003","title":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   httpapi.CodeInvalidRequest,
		},
		{
			name:       "rejects invalid urgency",
			user:       &user,
			body:       `{"case_type_id":"00000000-0000-0000-0000-000000000003","title":"x","urgency":"ASAP"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   httpapi.CodeInvalidRequest,
		},
		{
			name:       "rejects invalid case_type_id",
			user:       &user,
			body:       `{"case_type_id":"nope","title":"x"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   httpapi.CodeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeCaseStore{createResult: created}
			request := caseRequestWithID(http.MethodPost, "/api/v1/cases", "", tt.body, tt.user)
			response := httptest.NewRecorder()

			handler.CreateCase(store).ServeHTTP(response, request)

			assertCaseTypeAPIError(t, response, tt.wantStatus, tt.wantCode)
			if store.createCalls != 0 {
				t.Errorf("CreateCase calls = %d, want 0", store.createCalls)
			}
		})
	}

	t.Run("creator is owner and maker with creation audit event", func(t *testing.T) {
		store := &fakeCaseStore{createResult: created}
		body := `{"case_type_id":"00000000-0000-0000-0000-000000000003","title":"  Leaking pipe  "}`
		request := caseRequestWithID(http.MethodPost, "/api/v1/cases", "", body, &user)
		response := httptest.NewRecorder()

		handler.CreateCase(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusCreated, map[string]any{"data": caseJSON(created)})

		if store.createCalls != 1 {
			t.Fatalf("CreateCase calls = %d, want 1", store.createCalls)
		}
		arg := store.createArg
		if arg.CreatedBy != user.ID || arg.OwnerID != user.ID {
			t.Errorf("creator/owner = %v/%v, want both %v", arg.CreatedBy, arg.OwnerID, user.ID)
		}
		if arg.Title != "Leaking pipe" {
			t.Errorf("title = %q, want trimmed", arg.Title)
		}
		if arg.Urgency != "MEDIUM" {
			t.Errorf("urgency = %q, want MEDIUM default", arg.Urgency)
		}
		if arg.Description != "" {
			t.Errorf("description = %q, want empty default", arg.Description)
		}
		if arg.CaseTypeID != caseTypeID {
			t.Errorf("case_type_id = %v, want %v", arg.CaseTypeID, caseTypeID)
		}

		if store.participantCalls != 1 {
			t.Fatalf("CreateCaseParticipant calls = %d, want 1", store.participantCalls)
		}
		part := store.participantArg
		if part.CaseID != created.ID || part.UserID != user.ID || part.Role != "MAKER" || !part.Required || part.AssignedBy != user.ID {
			t.Errorf("maker participant arg = %+v, want role MAKER assigned to creator", part)
		}

		if store.auditCalls != 1 {
			t.Fatalf("AppendCaseAuditEvent calls = %d, want 1", store.auditCalls)
		}
		audit := store.auditArg
		if audit.CaseID != created.ID || audit.EventType != "CASE_CREATED" || audit.ActorID != user.ID || audit.ActorRole.String != "MAKER" {
			t.Errorf("audit event arg = %+v, want CASE_CREATED by MAKER", audit)
		}
	})
}

func TestListCases(t *testing.T) {
	user := caseTestUser(auth.SystemRoleUser)
	admin := caseTestUser(auth.SystemRoleAdmin)
	stored := []db.Case{caseTestCase(user.ID, handlerTestUUID(3))}

	t.Run("rejects unauthenticated request", func(t *testing.T) {
		store := &fakeCaseStore{}
		request := caseRequestWithID(http.MethodGet, "/api/v1/cases", "", "", nil)
		response := httptest.NewRecorder()

		handler.ListCases(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized)
	})

	t.Run("admin sees all cases", func(t *testing.T) {
		store := &fakeCaseStore{listResult: stored}
		request := caseRequestWithID(http.MethodGet, "/api/v1/cases", "", "", &admin)
		response := httptest.NewRecorder()

		handler.ListCases(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": []any{caseJSON(stored[0])}})
		if !store.listArg.IsAdmin {
			t.Error("IsAdmin = false, want true for admin")
		}
		if store.listArg.UserID != admin.ID {
			t.Errorf("UserID = %v, want %v", store.listArg.UserID, admin.ID)
		}
	})

	t.Run("regular user sees participant cases", func(t *testing.T) {
		store := &fakeCaseStore{listResult: stored}
		request := caseRequestWithID(http.MethodGet, "/api/v1/cases", "", "", &user)
		response := httptest.NewRecorder()

		handler.ListCases(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": []any{caseJSON(stored[0])}})
		if store.listArg.IsAdmin {
			t.Error("IsAdmin = true, want false for regular user")
		}
	})
}

func TestGetCase(t *testing.T) {
	user := caseTestUser(auth.SystemRoleUser)
	admin := caseTestUser(auth.SystemRoleAdmin)
	caseID := handlerTestUUID(4)
	stored := caseTestCase(user.ID, handlerTestUUID(3))
	target := "/api/v1/cases/00000000-0000-0000-0000-000000000004"

	t.Run("rejects invalid id", func(t *testing.T) {
		store := &fakeCaseStore{}
		request := caseRequestWithID(http.MethodGet, target, "nope", "", &user)
		response := httptest.NewRecorder()

		handler.GetCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	})

	t.Run("non-participant gets 404", func(t *testing.T) {
		store := &fakeCaseStore{isParticipant: false, getResult: stored}
		request := caseRequestWithID(http.MethodGet, target, caseID.String(), "", &user)
		response := httptest.NewRecorder()

		handler.GetCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
		if store.getCalls != 0 {
			t.Errorf("GetCase calls = %d, want 0 for non-participant", store.getCalls)
		}
	})

	t.Run("participant reads case", func(t *testing.T) {
		store := &fakeCaseStore{isParticipant: true, getResult: stored}
		request := caseRequestWithID(http.MethodGet, target, caseID.String(), "", &user)
		response := httptest.NewRecorder()

		handler.GetCase(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": caseJSON(stored)})
	})

	t.Run("admin reads without participant check", func(t *testing.T) {
		store := &fakeCaseStore{getResult: stored}
		request := caseRequestWithID(http.MethodGet, target, caseID.String(), "", &admin)
		response := httptest.NewRecorder()

		handler.GetCase(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": caseJSON(stored)})
		if store.isParticipantCalls != 0 {
			t.Errorf("IsActiveCaseParticipant calls = %d, want 0 for admin", store.isParticipantCalls)
		}
	})
}

func TestUpdateCase(t *testing.T) {
	user := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4)
	target := "/api/v1/cases/00000000-0000-0000-0000-000000000004"
	draft := caseTestCase(user.ID, handlerTestUUID(3))

	t.Run("rejects immutable owner_id", func(t *testing.T) {
		store := &fakeCaseStore{isParticipant: true, getResult: draft}
		body := `{"owner_id":"00000000-0000-0000-0000-000000000008"}`
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), body, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
		if store.getCalls != 0 {
			t.Errorf("GetCase calls = %d, want 0 for immutable field rejection", store.getCalls)
		}
	})

	t.Run("rejects empty body", func(t *testing.T) {
		store := &fakeCaseStore{}
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), `{}`, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	})

	t.Run("non-participant gets 404", func(t *testing.T) {
		store := &fakeCaseStore{isParticipant: false}
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), `{"title":"x"}`, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
	})

	t.Run("non-DRAFT case returns 409", func(t *testing.T) {
		submitted := draft
		submitted.Status = "SUBMITTED"
		store := &fakeCaseStore{isParticipant: true, getResult: submitted}
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), `{"title":"x"}`, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
		if store.updateCalls != 0 {
			t.Errorf("UpdateCase calls = %d, want 0 for non-DRAFT", store.updateCalls)
		}
	})

	t.Run("rejects invalid urgency", func(t *testing.T) {
		store := &fakeCaseStore{isParticipant: true, getResult: draft}
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), `{"urgency":"URGENT"}`, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	})

	t.Run("updates DRAFT preserving untouched fields", func(t *testing.T) {
		updated := draft
		updated.Title = "New title"
		store := &fakeCaseStore{isParticipant: true, getResult: draft, updateResult: updated}
		request := caseRequestWithID(http.MethodPatch, target, caseID.String(), `{"title":"  New title  "}`, &user)
		response := httptest.NewRecorder()

		handler.UpdateCase(store).ServeHTTP(response, request)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": caseJSON(updated)})
		if store.updateCalls != 1 {
			t.Fatalf("UpdateCase calls = %d, want 1", store.updateCalls)
		}
		arg := store.updateArg
		if arg.Title != "New title" {
			t.Errorf("title = %q, want trimmed", arg.Title)
		}
		if arg.Description != draft.Description || arg.Urgency != draft.Urgency || arg.CaseTypeID != draft.CaseTypeID {
			t.Errorf("untouched fields changed: arg = %+v, want from stored case", arg)
		}
		if arg.ID != draft.ID {
			t.Errorf("id = %v, want %v", arg.ID, draft.ID)
		}
	})
}
