package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakePolicyAPIStore struct {
	policies      []db.Policy
	listErr       error
	createdPolicy db.Policy
	createErr     error
	auditErr      error

	listCalls   int
	createCalls int
	auditCalls  int
	createArg   db.CreatePolicyParams
	auditArg    db.AppendPolicyAuditEventParams
}

func (f *fakePolicyAPIStore) ListPolicies(context.Context) ([]db.Policy, error) {
	f.listCalls++
	return f.policies, f.listErr
}

func (f *fakePolicyAPIStore) CreatePolicy(_ context.Context, arg db.CreatePolicyParams) (db.Policy, error) {
	f.createCalls++
	f.createArg = arg
	return f.createdPolicy, f.createErr
}

func (f *fakePolicyAPIStore) AppendPolicyAuditEvent(_ context.Context, arg db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, f.auditErr
}

func TestListPoliciesAllowsAuthenticatedUser(t *testing.T) {
	createdAt := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	store := &fakePolicyAPIStore{policies: []db.Policy{
		{
			ID:          handlerTestUUID(1),
			Code:        "OPS-001",
			Title:       "Operational Controls",
			Domain:      "OPERATIONS",
			CaseTypeID:  handlerTestUUID(2),
			Description: pgtype.Text{String: "Required controls", Valid: true},
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		},
		{
			ID:        handlerTestUUID(3),
			Code:      "GEN-001",
			Title:     "General Controls",
			Domain:    "GENERAL",
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
	}}
	h := auth.RequireAuth(handler.ListPolicies(store))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertJSONResponse(t, response, http.StatusOK, map[string]any{
		"data": []any{
			map[string]any{
				"id":           "00000000-0000-0000-0000-000000000001",
				"code":         "OPS-001",
				"title":        "Operational Controls",
				"domain":       "OPERATIONS",
				"case_type_id": "00000000-0000-0000-0000-000000000002",
				"description":  "Required controls",
				"created_at":   "2026-10-05T10:00:00Z",
				"updated_at":   "2026-10-05T11:00:00Z",
			},
			map[string]any{
				"id":           "00000000-0000-0000-0000-000000000003",
				"code":         "GEN-001",
				"title":        "General Controls",
				"domain":       "GENERAL",
				"case_type_id": nil,
				"description":  nil,
				"created_at":   "2026-10-05T10:00:00Z",
				"updated_at":   "2026-10-05T11:00:00Z",
			},
		},
	})
	if store.listCalls != 1 {
		t.Errorf("ListPolicies calls = %d, want 1", store.listCalls)
	}
}

func TestListPoliciesReturnsEmptyArray(t *testing.T) {
	store := &fakePolicyAPIStore{}
	h := auth.RequireAuth(handler.ListPolicies(store))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": []any{}})
}

func TestCreatePolicyAllowsAdmin(t *testing.T) {
	actor := auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleAdmin}
	caseTypeID := handlerTestUUID(4)
	createdAt := time.Date(2026, time.October, 6, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		body         string
		created      db.Policy
		wantCaseType pgtype.UUID
		wantDesc     pgtype.Text
		wantCaseJSON any
		wantDescJSON any
	}{
		{
			name: "maps optional fields",
			body: `{"code":"  OPS-001  ","title":"  Operational Controls  ","domain":"  OPERATIONS  ",` +
				`"case_type_id":"00000000-0000-0000-0000-000000000004","description":" Required controls "}`,
			created: db.Policy{
				ID:          handlerTestUUID(1),
				Code:        "OPS-001",
				Title:       "Operational Controls",
				Domain:      "OPERATIONS",
				CaseTypeID:  caseTypeID,
				Description: pgtype.Text{String: "Required controls", Valid: true},
				CreatedAt:   createdAt,
				UpdatedAt:   createdAt,
			},
			wantCaseType: caseTypeID,
			wantDesc:     pgtype.Text{String: "Required controls", Valid: true},
			wantCaseJSON: "00000000-0000-0000-0000-000000000004",
			wantDescJSON: "Required controls",
		},
		{
			name: "maps empty optional fields to null",
			body: `{"code":"GEN-001","title":"General Controls","domain":"GENERAL",` +
				`"case_type_id":"","description":""}`,
			created: db.Policy{
				ID:        handlerTestUUID(2),
				Code:      "GEN-001",
				Title:     "General Controls",
				Domain:    "GENERAL",
				CreatedAt: createdAt,
				UpdatedAt: createdAt,
			},
			wantCaseType: pgtype.UUID{},
			wantDesc:     pgtype.Text{},
			wantCaseJSON: nil,
			wantDescJSON: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePolicyAPIStore{createdPolicy: tt.created}
			h := auth.RequireAdmin(handler.CreatePolicy(store))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(auth.WithUser(request.Context(), actor))
			response := httptest.NewRecorder()

			h.ServeHTTP(response, request)

			assertJSONResponse(t, response, http.StatusCreated, map[string]any{
				"data": map[string]any{
					"id":           tt.created.ID.String(),
					"code":         tt.created.Code,
					"title":        tt.created.Title,
					"domain":       tt.created.Domain,
					"case_type_id": tt.wantCaseJSON,
					"description":  tt.wantDescJSON,
					"created_at":   "2026-10-06T09:00:00Z",
					"updated_at":   "2026-10-06T09:00:00Z",
				},
			})
			if store.createCalls != 1 {
				t.Fatalf("CreatePolicy calls = %d, want 1", store.createCalls)
			}
			if !store.createArg.ID.Valid || store.createArg.ID.Bytes == ([16]byte{}) {
				t.Errorf("CreatePolicy ID = %+v, want a valid generated UUID", store.createArg.ID)
			}
			wantCreateArg := db.CreatePolicyParams{
				ID:          store.createArg.ID,
				Code:        tt.created.Code,
				Title:       tt.created.Title,
				Domain:      tt.created.Domain,
				CaseTypeID:  tt.wantCaseType,
				Description: tt.wantDesc,
			}
			if !reflect.DeepEqual(store.createArg, wantCreateArg) {
				t.Errorf("CreatePolicy arg = %+v, want %+v", store.createArg, wantCreateArg)
			}

			if store.auditCalls != 1 {
				t.Fatalf("AppendPolicyAuditEvent calls = %d, want 1", store.auditCalls)
			}
			if !store.auditArg.ID.Valid || store.auditArg.ID.Bytes == ([16]byte{}) {
				t.Errorf("audit event ID = %+v, want a valid generated UUID", store.auditArg.ID)
			}
			wantAuditArg := db.AppendPolicyAuditEventParams{
				ID:        store.auditArg.ID,
				PolicyID:  tt.created.ID,
				EventType: "POLICY_CREATED",
				ActorID:   actor.ID,
				Metadata:  []byte(`{}`),
			}
			if !reflect.DeepEqual(store.auditArg, wantAuditArg) {
				t.Errorf("AppendPolicyAuditEvent arg = %+v, want %+v", store.auditArg, wantAuditArg)
			}
		})
	}
}

func TestCreatePolicyRejectsNonAdminAtMiddleware(t *testing.T) {
	// CreatePolicy intentionally has no role check: the registered route composes
	// it with RequireAdmin, whose behavior is also covered by auth guard tests.
	store := &fakePolicyAPIStore{}
	h := auth.RequireAdmin(handler.CreatePolicy(store))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(
		`{"code":"OPS-001","title":"Operational Controls","domain":"OPERATIONS"}`,
	))
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(8),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertPolicyAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden, "")
	if store.createCalls != 0 || store.auditCalls != 0 {
		t.Errorf("store calls = create %d, audit %d; want zero", store.createCalls, store.auditCalls)
	}
}

func TestCreatePolicyReturnsConflictForDuplicateCode(t *testing.T) {
	store := &fakePolicyAPIStore{createErr: &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "policies_code_key",
	}}
	response := serveCreatePolicy(t, store, `{"code":"OPS-001","title":"Duplicate","domain":"OPERATIONS"}`)

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "Policy code already exists.")
	if store.createCalls != 1 {
		t.Errorf("CreatePolicy calls = %d, want 1", store.createCalls)
	}
	if store.auditCalls != 0 {
		t.Errorf("AppendPolicyAuditEvent calls = %d, want 0", store.auditCalls)
	}
}

func TestCreatePolicyRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "code", body: `{"code":"  ","title":"Policy","domain":"OPERATIONS"}`},
		{name: "title", body: `{"code":"OPS-001","title":"  ","domain":"OPERATIONS"}`},
		{name: "domain", body: `{"code":"OPS-001","title":"Policy","domain":"  "}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePolicyAPIStore{}
			response := serveCreatePolicy(t, store, tt.body)

			assertPolicyAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError, "")
			if store.createCalls != 0 || store.auditCalls != 0 {
				t.Errorf("store calls = create %d, audit %d; want zero", store.createCalls, store.auditCalls)
			}
		})
	}
}

func TestCreatePolicyRejectsInvalidCaseTypeID(t *testing.T) {
	store := &fakePolicyAPIStore{}
	response := serveCreatePolicy(t, store,
		`{"code":"OPS-001","title":"Policy","domain":"OPERATIONS","case_type_id":"not-a-uuid"}`,
	)

	assertPolicyAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError, "")
	if store.createCalls != 0 || store.auditCalls != 0 {
		t.Errorf("store calls = create %d, audit %d; want zero", store.createCalls, store.auditCalls)
	}
}

func serveCreatePolicy(t *testing.T, store *fakePolicyAPIStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := auth.RequireAdmin(handler.CreatePolicy(store))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func assertPolicyAPIError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code httpapi.ErrorCode,
	message string,
) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var body httpapi.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v; body=%q", err, response.Body.String())
	}
	if body.Error.Code != code {
		t.Errorf("error code = %q, want %q", body.Error.Code, code)
	}
	if message != "" && body.Error.Message != message {
		t.Errorf("error message = %q, want %q", body.Error.Message, message)
	}
}
