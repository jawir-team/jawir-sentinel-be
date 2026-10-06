package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeUserAPIStore struct {
	listedUsers []db.ListUsersRow
	listErr     error
	createdUser db.User
	createErr   error
	storedUser  db.User
	getErr      error
	adminCount  int64
	countErr    error
	updatedUser db.User
	updateErr   error

	listCalls   int
	listArg     db.ListUsersParams
	createCalls int
	createArg   db.CreateUserParams
	getCalls    int
	gotUserID   pgtype.UUID
	countCalls  int
	updateCalls int
	updateArg   db.UpdateUserParams
}

func (f *fakeUserAPIStore) ListUsers(_ context.Context, arg db.ListUsersParams) ([]db.ListUsersRow, error) {
	f.listCalls++
	f.listArg = arg
	return f.listedUsers, f.listErr
}

func (f *fakeUserAPIStore) CreateUser(_ context.Context, arg db.CreateUserParams) (db.User, error) {
	f.createCalls++
	f.createArg = arg
	return f.createdUser, f.createErr
}

func (f *fakeUserAPIStore) GetUser(_ context.Context, id pgtype.UUID) (db.User, error) {
	f.getCalls++
	f.gotUserID = id
	return f.storedUser, f.getErr
}

func (f *fakeUserAPIStore) CountActiveAdmins(context.Context) (int64, error) {
	f.countCalls++
	return f.adminCount, f.countErr
}

func (f *fakeUserAPIStore) UpdateUser(_ context.Context, arg db.UpdateUserParams) (db.User, error) {
	f.updateCalls++
	f.updateArg = arg
	return f.updatedUser, f.updateErr
}

func TestListUsersAllowsAuthenticatedUserAndReturnsSafeDirectory(t *testing.T) {
	userID := handlerTestUUID(1)
	unitID := handlerTestUUID(2)
	store := &fakeUserAPIStore{listedUsers: []db.ListUsersRow{
		{
			ID:         userID,
			UnitID:     unitID,
			Name:       "Alice Example",
			Email:      "alice@example.test",
			Status:     "ACTIVE",
			SystemRole: auth.SystemRoleUser,
		},
	}}
	h := auth.RequireAuth(handler.ListUsers(store))
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/users?q=alice&status=ACTIVE&limit=10&offset=5",
		nil,
	)
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
				"unit_id":     "00000000-0000-0000-0000-000000000002",
				"name":        "Alice Example",
				"email":       "alice@example.test",
				"status":      "ACTIVE",
				"system_role": auth.SystemRoleUser,
			},
		},
	})
	if store.listCalls != 1 {
		t.Fatalf("ListUsers calls = %d, want 1", store.listCalls)
	}
	if store.listArg.Search != "alice" || store.listArg.Status != "ACTIVE" || store.listArg.Limit != 10 || store.listArg.Offset != 5 {
		t.Errorf("ListUsers arg = %+v, want search=alice status=ACTIVE limit=10 offset=5", store.listArg)
	}
	if strings.Contains(response.Body.String(), "firebase_uid") {
		t.Fatalf("directory response exposed firebase_uid: %s", response.Body.String())
	}
}

func TestCreateUserRejectsNonAdminUser(t *testing.T) {
	store := &fakeUserAPIStore{}
	h := auth.RequireAdmin(handler.CreateUser(store))
	request := newCreateUserRequest(auth.SystemRoleUser)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertUserAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
	if store.createCalls != 0 {
		t.Errorf("CreateUser calls = %d, want 0", store.createCalls)
	}
}

func TestCreateUserAllowsAdmin(t *testing.T) {
	createdID := handlerTestUUID(3)
	unitID := handlerTestUUID(2)
	store := &fakeUserAPIStore{createdUser: db.User{
		ID:          createdID,
		UnitID:      unitID,
		FirebaseUID: "firebase-private-uid",
		Name:        "Alice Example",
		Email:       "alice@example.test",
		Status:      "ACTIVE",
		SystemRole:  auth.SystemRoleUser,
	}}
	h := auth.RequireAdmin(handler.CreateUser(store))
	request := newCreateUserRequest(auth.SystemRoleUser)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()

	h.ServeHTTP(response, request)

	assertJSONResponse(t, response, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":          "00000000-0000-0000-0000-000000000003",
			"unit_id":     "00000000-0000-0000-0000-000000000002",
			"name":        "Alice Example",
			"email":       "alice@example.test",
			"status":      "ACTIVE",
			"system_role": auth.SystemRoleUser,
		},
	})
	if store.createCalls != 1 {
		t.Fatalf("CreateUser calls = %d, want 1", store.createCalls)
	}
	if !store.createArg.ID.Valid || store.createArg.ID.Bytes == ([16]byte{}) {
		t.Errorf("CreateUser ID = %+v, want a valid generated UUID", store.createArg.ID)
	}
	if store.createArg.UnitID != unitID ||
		store.createArg.FirebaseUID != "firebase-private-uid" ||
		store.createArg.Name != "Alice Example" ||
		store.createArg.Email != "alice@example.test" ||
		store.createArg.Status != "ACTIVE" ||
		store.createArg.SystemRole != auth.SystemRoleUser {
		t.Errorf("CreateUser arg = %+v, want request values", store.createArg)
	}
	if strings.Contains(response.Body.String(), "firebase_uid") || strings.Contains(response.Body.String(), "firebase-private-uid") {
		t.Fatalf("create response exposed firebase UID: %s", response.Body.String())
	}
}

func TestCreateUserReturnsConflictForDuplicateIdentity(t *testing.T) {
	for _, tt := range []struct {
		name       string
		constraint string
	}{
		{name: "email", constraint: "users_email_key"},
		{name: "firebase UID", constraint: "users_firebase_uid_key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeUserAPIStore{createErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: tt.constraint,
			}}
			h := auth.RequireAdmin(handler.CreateUser(store))
			request := newCreateUserRequest(auth.SystemRoleUser)
			request = request.WithContext(auth.WithUser(request.Context(), auth.User{
				ID:         handlerTestUUID(9),
				SystemRole: auth.SystemRoleAdmin,
			}))
			response := httptest.NewRecorder()

			h.ServeHTTP(response, request)

			assertUserAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
			if store.createCalls != 1 {
				t.Errorf("CreateUser calls = %d, want 1", store.createCalls)
			}
		})
	}
}

func TestUpdateUserRejectsDemotingLastActiveAdmin(t *testing.T) {
	targetID := handlerTestUUID(4)
	store := &fakeUserAPIStore{
		storedUser: db.User{
			ID:          targetID,
			UnitID:      handlerTestUUID(2),
			FirebaseUID: "firebase-admin-private",
			Name:        "Last Admin",
			Email:       "admin@example.test",
			Status:      "ACTIVE",
			SystemRole:  auth.SystemRoleAdmin,
		},
		adminCount: 1,
	}
	router := chi.NewRouter()
	router.With(auth.RequireAdmin).Patch("/{id}", handler.UpdateUser(store))
	request := httptest.NewRequest(
		http.MethodPatch,
		"/"+uuidString(targetID),
		strings.NewReader(`{"system_role":"USER"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assertUserAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	if store.getCalls != 1 || store.gotUserID != targetID {
		t.Errorf("GetUser calls = %d, ID = %+v; want 1 with %+v", store.getCalls, store.gotUserID, targetID)
	}
	if store.countCalls != 1 {
		t.Errorf("CountActiveAdmins calls = %d, want 1", store.countCalls)
	}
	if store.updateCalls != 0 {
		t.Errorf("UpdateUser calls = %d, want 0", store.updateCalls)
	}
}

func newCreateUserRequest(role string) *http.Request {
	body := `{"firebase_uid":"firebase-private-uid","name":"Alice Example","email":"alice@example.test","unit_id":"00000000-0000-0000-0000-000000000002","status":"ACTIVE","system_role":"` + role + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	bytes := id.Bytes
	const hex = "0123456789abcdef"
	encoded := make([]byte, 36)
	positions := [16]int{0, 2, 4, 6, 9, 11, 14, 16, 19, 21, 24, 26, 28, 30, 32, 34}
	for index, value := range bytes {
		position := positions[index]
		encoded[position] = hex[value>>4]
		encoded[position+1] = hex[value&0x0f]
	}
	encoded[8], encoded[13], encoded[18], encoded[23] = '-', '-', '-', '-'
	return string(encoded)
}

func assertUserAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code httpapi.ErrorCode) {
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
