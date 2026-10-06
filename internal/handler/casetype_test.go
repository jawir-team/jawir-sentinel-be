package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeCaseTypeAPIStore struct {
	caseTypes       []db.CaseType
	listErr         error
	createdCaseType db.CaseType
	createErr       error

	listCalls   int
	createCalls int
	createArg   db.CreateCaseTypeParams
}

func (f *fakeCaseTypeAPIStore) ListCaseTypes(context.Context) ([]db.CaseType, error) {
	f.listCalls++
	return f.caseTypes, f.listErr
}

func (f *fakeCaseTypeAPIStore) CreateCaseType(_ context.Context, arg db.CreateCaseTypeParams) (db.CaseType, error) {
	f.createCalls++
	f.createArg = arg
	return f.createdCaseType, f.createErr
}

func TestListCaseTypes(t *testing.T) {
	tests := []struct {
		name       string
		user       *auth.User
		store      *fakeCaseTypeAPIStore
		wantStatus int
		wantBody   map[string]any
		wantCode   httpapi.ErrorCode
		wantCalls  int
	}{
		{
			name:       "rejects request without user",
			store:      &fakeCaseTypeAPIStore{},
			wantStatus: http.StatusUnauthorized,
			wantCode:   httpapi.CodeUnauthorized,
		},
		{
			name: "allows authenticated user",
			user: &auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleUser},
			store: &fakeCaseTypeAPIStore{caseTypes: []db.CaseType{
				{
					ID:          handlerTestUUID(1),
					Code:        "INCIDENT",
					Name:        "Incident",
					Description: pgtype.Text{String: "Operational incident", Valid: true},
				},
				{
					ID:          handlerTestUUID(2),
					Code:        "EXCEPTION",
					Name:        "Exception",
					Description: pgtype.Text{},
				},
			}},
			wantStatus: http.StatusOK,
			wantCalls:  1,
			wantBody: map[string]any{
				"data": []any{
					map[string]any{
						"id":          "00000000-0000-0000-0000-000000000001",
						"code":        "INCIDENT",
						"name":        "Incident",
						"description": "Operational incident",
					},
					map[string]any{
						"id":          "00000000-0000-0000-0000-000000000002",
						"code":        "EXCEPTION",
						"name":        "Exception",
						"description": nil,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/case-types", nil)
			if tt.user != nil {
				request = request.WithContext(auth.WithUser(request.Context(), *tt.user))
			}
			response := httptest.NewRecorder()

			handler.ListCaseTypes(tt.store).ServeHTTP(response, request)

			if tt.wantCode != "" {
				assertCaseTypeAPIError(t, response, tt.wantStatus, tt.wantCode)
			} else {
				assertJSONResponse(t, response, tt.wantStatus, tt.wantBody)
			}
			if tt.store.listCalls != tt.wantCalls {
				t.Errorf("ListCaseTypes calls = %d, want %d", tt.store.listCalls, tt.wantCalls)
			}
		})
	}
}

func TestCreateCaseType(t *testing.T) {
	admin := auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleAdmin}
	regularUser := auth.User{ID: handlerTestUUID(8), SystemRole: auth.SystemRoleUser}
	created := db.CaseType{
		ID:          handlerTestUUID(3),
		Code:        "INCIDENT",
		Name:        "Incident",
		Description: pgtype.Text{String: "Operational incident", Valid: true},
	}
	tests := []struct {
		name       string
		user       auth.User
		body       string
		store      *fakeCaseTypeAPIStore
		wantStatus int
		wantBody   map[string]any
		wantCode   httpapi.ErrorCode
		wantCalls  int
		wantArg    *db.CreateCaseTypeParams
	}{
		{
			name:       "rejects non-admin user",
			user:       regularUser,
			body:       `{"code":"INCIDENT","name":"Incident"}`,
			store:      &fakeCaseTypeAPIStore{},
			wantStatus: http.StatusForbidden,
			wantCode:   httpapi.CodeForbidden,
		},
		{
			name:       "allows admin",
			user:       admin,
			body:       `{"code":"  INCIDENT  ","name":"  Incident  ","description":"Operational incident"}`,
			store:      &fakeCaseTypeAPIStore{createdCaseType: created},
			wantStatus: http.StatusCreated,
			wantCalls:  1,
			wantArg: &db.CreateCaseTypeParams{
				Code:        "INCIDENT",
				Name:        "Incident",
				Description: pgtype.Text{String: "Operational incident", Valid: true},
			},
			wantBody: map[string]any{
				"data": map[string]any{
					"id":          "00000000-0000-0000-0000-000000000003",
					"code":        "INCIDENT",
					"name":        "Incident",
					"description": "Operational incident",
				},
			},
		},
		{
			name: "returns conflict for duplicate code",
			user: admin,
			body: `{"code":"INCIDENT","name":"Duplicate Incident"}`,
			store: &fakeCaseTypeAPIStore{createErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "case_types_code_key",
			}},
			wantStatus: http.StatusConflict,
			wantCode:   httpapi.CodeConflict,
			wantCalls:  1,
			wantArg: &db.CreateCaseTypeParams{
				Code: "INCIDENT",
				Name: "Duplicate Incident",
			},
		},
		{
			name:       "rejects missing code",
			user:       admin,
			body:       `{"code":"   ","name":"Incident"}`,
			store:      &fakeCaseTypeAPIStore{},
			wantStatus: http.StatusBadRequest,
			wantCode:   httpapi.CodeInvalidRequest,
		},
		{
			name:       "rejects missing name",
			user:       admin,
			body:       `{"code":"INCIDENT","name":"   "}`,
			store:      &fakeCaseTypeAPIStore{},
			wantStatus: http.StatusBadRequest,
			wantCode:   httpapi.CodeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/case-types", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(auth.WithUser(request.Context(), tt.user))
			response := httptest.NewRecorder()

			handler.CreateCaseType(tt.store).ServeHTTP(response, request)

			if tt.wantCode != "" {
				assertCaseTypeAPIError(t, response, tt.wantStatus, tt.wantCode)
			} else {
				assertJSONResponse(t, response, tt.wantStatus, tt.wantBody)
			}
			if tt.store.createCalls != tt.wantCalls {
				t.Fatalf("CreateCaseType calls = %d, want %d", tt.store.createCalls, tt.wantCalls)
			}
			if tt.wantArg != nil {
				if !tt.store.createArg.ID.Valid || tt.store.createArg.ID.Bytes == ([16]byte{}) {
					t.Errorf("CreateCaseType ID = %+v, want a valid generated UUID", tt.store.createArg.ID)
				}
				wantArg := *tt.wantArg
				wantArg.ID = tt.store.createArg.ID
				if !reflect.DeepEqual(tt.store.createArg, wantArg) {
					t.Errorf("CreateCaseType arg = %+v, want %+v", tt.store.createArg, wantArg)
				}
			}
		})
	}
}

func assertCaseTypeAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code httpapi.ErrorCode) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	var body httpapi.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v; body=%q", err, response.Body.String())
	}
	if body.Error.Code != code {
		t.Errorf("error code = %q, want %q", body.Error.Code, code)
	}
	if body.Error.Message == "" {
		t.Error("error message must not be empty")
	}
	if body.Error.Details == nil || len(body.Error.Details) != 0 {
		t.Errorf("error details = %#v, want empty object", body.Error.Details)
	}
}
