package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
)

type fakeUnitStore struct {
	unit  db.Unit
	err   error
	calls int
	gotID pgtype.UUID
}

func (f *fakeUnitStore) GetUnit(_ context.Context, id pgtype.UUID) (db.Unit, error) {
	f.calls++
	f.gotID = id
	return f.unit, f.err
}

func TestGetMeReturnsAuthenticatedUser(t *testing.T) {
	userID := handlerTestUUID(1)
	unitID := handlerTestUUID(2)

	for _, role := range []string{auth.SystemRoleUser, auth.SystemRoleAdmin} {
		t.Run(role, func(t *testing.T) {
			store := &fakeUnitStore{unit: db.Unit{
				ID:          unitID,
				Code:        "OPS",
				Name:        "Operations",
				Description: pgtype.Text{String: "internal unit description", Valid: true},
			}}
			user := auth.User{
				ID:          userID,
				FirebaseUID: "firebase-private-uid",
				Name:        "Ferdian",
				Email:       "ferdian@example.com",
				UnitID:      unitID,
				SystemRole:  role,
			}

			response := serveGetMe(handler.GetMe(store), &user)

			assertJSONResponse(t, response, http.StatusOK, map[string]any{
				"data": map[string]any{
					"id":          "00000000-0000-0000-0000-000000000001",
					"name":        "Ferdian",
					"email":       "ferdian@example.com",
					"system_role": role,
					"unit": map[string]any{
						"id":   "00000000-0000-0000-0000-000000000002",
						"code": "OPS",
						"name": "Operations",
					},
				},
			})
			if store.calls != 1 || store.gotID != unitID {
				t.Errorf("GetUnit calls = %d, ID = %v; want 1 call with %v", store.calls, store.gotID, unitID)
			}
			for _, privateValue := range []string{"firebase-private-uid", "internal unit description"} {
				if strings.Contains(response.Body.String(), privateValue) {
					t.Errorf("response leaked private value %q: %s", privateValue, response.Body.String())
				}
			}
		})
	}
}

func TestGetMeReturnsNullUnitWhenUserHasNoUnit(t *testing.T) {
	store := &fakeUnitStore{}
	user := auth.User{
		ID:          handlerTestUUID(1),
		FirebaseUID: "firebase-private-uid",
		Name:        "No Unit User",
		Email:       "no-unit@example.com",
		SystemRole:  auth.SystemRoleUser,
		UnitID:      pgtype.UUID{},
	}

	response := serveGetMe(handler.GetMe(store), &user)

	assertJSONResponse(t, response, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":          "00000000-0000-0000-0000-000000000001",
			"name":        "No Unit User",
			"email":       "no-unit@example.com",
			"system_role": auth.SystemRoleUser,
			"unit":        nil,
		},
	})
	if store.calls != 0 {
		t.Errorf("GetUnit calls = %d, want 0 for an invalid unit ID", store.calls)
	}
}

func TestGetMeRejectsMissingAuthenticatedUser(t *testing.T) {
	store := &fakeUnitStore{}

	response := serveGetMe(handler.GetMe(store), nil)

	assertJSONResponse(t, response, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"code":    "UNAUTHORIZED",
			"message": "Authentication credentials are required.",
			"details": map[string]any{},
		},
	})
	if store.calls != 0 {
		t.Errorf("GetUnit calls = %d, want 0", store.calls)
	}
}

func TestGetMeReturnsGenericInternalErrorWhenUnitLookupFails(t *testing.T) {
	unitID := handlerTestUUID(2)
	user := auth.User{
		ID:          handlerTestUUID(1),
		FirebaseUID: "firebase-private-uid",
		Name:        "Ferdian",
		Email:       "ferdian@example.com",
		UnitID:      unitID,
		SystemRole:  auth.SystemRoleUser,
	}
	tests := []struct {
		name        string
		err         error
		privateText string
	}{
		{
			name:        "database error",
			err:         errors.New("database password and query detail"),
			privateText: "database password",
		},
		{
			name:        "unit not found",
			err:         pgx.ErrNoRows,
			privateText: "no rows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeUnitStore{err: tt.err}

			response := serveGetMe(handler.GetMe(store), &user)

			assertJSONResponse(t, response, http.StatusInternalServerError, map[string]any{
				"error": map[string]any{
					"code":    "INTERNAL_ERROR",
					"message": "An internal error occurred.",
					"details": map[string]any{},
				},
			})
			if store.calls != 1 || store.gotID != unitID {
				t.Errorf("GetUnit calls = %d, ID = %v; want 1 call with %v", store.calls, store.gotID, unitID)
			}
			if strings.Contains(response.Body.String(), tt.privateText) {
				t.Errorf("response leaked store error %q: %s", tt.privateText, response.Body.String())
			}
		})
	}
}

func serveGetMe(handler http.Handler, user *auth.User) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if user != nil {
		request = request.WithContext(auth.WithUser(request.Context(), *user))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertJSONResponse(t *testing.T, response *httptest.ResponseRecorder, status int, want map[string]any) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response body: %v; body=%q", err, response.Body.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response body = %#v, want %#v", got, want)
	}
}

func handlerTestUUID(lastByte byte) pgtype.UUID {
	var id [16]byte
	id[15] = lastByte
	return pgtype.UUID{Bytes: id, Valid: true}
}
