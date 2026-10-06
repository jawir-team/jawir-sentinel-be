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

type fakeUnitAPIStore struct {
	units       []db.Unit
	listErr     error
	createdUnit db.Unit
	createErr   error

	listCalls   int
	createCalls int
	createArg   db.CreateUnitParams
}

func (f *fakeUnitAPIStore) ListUnits(context.Context) ([]db.Unit, error) {
	f.listCalls++
	return f.units, f.listErr
}

func (f *fakeUnitAPIStore) CreateUnit(_ context.Context, arg db.CreateUnitParams) (db.Unit, error) {
	f.createCalls++
	f.createArg = arg
	return f.createdUnit, f.createErr
}

func TestListUnitsAllowsAuthenticatedUser(t *testing.T) {
	store := &fakeUnitAPIStore{units: []db.Unit{
		{
			ID:          handlerTestUUID(1),
			Code:        "OPS",
			Name:        "Operations",
			Description: pgtype.Text{String: "Operational response unit", Valid: true},
		},
		{
			ID:          handlerTestUUID(2),
			Code:        "FIN",
			Name:        "Finance",
			Description: pgtype.Text{},
		},
	}}
	h := auth.RequireAuth(handler.ListUnits(store))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/units", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertJSONResponse(t, response, http.StatusOK, map[string]any{
		"data": []any{
			map[string]any{
				"id":          "00000000-0000-0000-0000-000000000001",
				"code":        "OPS",
				"name":        "Operations",
				"description": "Operational response unit",
			},
			map[string]any{
				"id":          "00000000-0000-0000-0000-000000000002",
				"code":        "FIN",
				"name":        "Finance",
				"description": nil,
			},
		},
	})
	if store.listCalls != 1 {
		t.Errorf("ListUnits calls = %d, want 1", store.listCalls)
	}
	if store.createCalls != 0 {
		t.Errorf("CreateUnit calls = %d, want 0", store.createCalls)
	}
}

func TestCreateUnitRejectsNonAdminUser(t *testing.T) {
	store := &fakeUnitAPIStore{}
	h := auth.RequireAdmin(handler.CreateUnit(store))
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/units",
		strings.NewReader(`{"code":"OPS","name":"Operations"}`),
	)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertUnitAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	if store.createCalls != 0 {
		t.Errorf("CreateUnit calls = %d, want 0", store.createCalls)
	}
}

func TestCreateUnitAllowsAdmin(t *testing.T) {
	createdID := handlerTestUUID(3)
	store := &fakeUnitAPIStore{createdUnit: db.Unit{
		ID:          createdID,
		Code:        "OPS",
		Name:        "Operations",
		Description: pgtype.Text{String: "Operational response unit", Valid: true},
	}}
	h := auth.RequireAdmin(handler.CreateUnit(store))
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/units",
		strings.NewReader(`{"code":"OPS","name":"Operations","description":"Operational response unit"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertJSONResponse(t, response, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":          "00000000-0000-0000-0000-000000000003",
			"code":        "OPS",
			"name":        "Operations",
			"description": "Operational response unit",
		},
	})
	if store.createCalls != 1 {
		t.Fatalf("CreateUnit calls = %d, want 1", store.createCalls)
	}
	if !store.createArg.ID.Valid || store.createArg.ID.Bytes == ([16]byte{}) {
		t.Errorf("CreateUnit ID = %+v, want a valid generated UUID", store.createArg.ID)
	}
	wantArg := db.CreateUnitParams{
		ID:          store.createArg.ID,
		Code:        "OPS",
		Name:        "Operations",
		Description: pgtype.Text{String: "Operational response unit", Valid: true},
	}
	if !reflect.DeepEqual(store.createArg, wantArg) {
		t.Errorf("CreateUnit arg = %+v, want %+v", store.createArg, wantArg)
	}
}

func TestCreateUnitReturnsConflictForDuplicateCode(t *testing.T) {
	store := &fakeUnitAPIStore{createErr: &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "units_code_key",
	}}
	h := auth.RequireAdmin(handler.CreateUnit(store))
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/units",
		strings.NewReader(`{"code":"OPS","name":"Duplicate Operations"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertUnitAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	if store.createCalls != 1 {
		t.Fatalf("CreateUnit calls = %d, want 1", store.createCalls)
	}
	if !store.createArg.ID.Valid || store.createArg.ID.Bytes == ([16]byte{}) {
		t.Errorf("CreateUnit ID = %+v, want a valid generated UUID", store.createArg.ID)
	}
	wantArg := db.CreateUnitParams{
		ID:          store.createArg.ID,
		Code:        "OPS",
		Name:        "Duplicate Operations",
		Description: pgtype.Text{},
	}
	if !reflect.DeepEqual(store.createArg, wantArg) {
		t.Errorf("CreateUnit arg = %+v, want %+v", store.createArg, wantArg)
	}
}

func assertUnitAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code httpapi.ErrorCode) {
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
