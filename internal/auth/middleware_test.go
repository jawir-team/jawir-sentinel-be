package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeTokenVerifier struct {
	uid      string
	err      error
	calls    int
	gotToken string
}

func (f *fakeTokenVerifier) VerifyIDToken(_ context.Context, token string) (string, error) {
	f.calls++
	f.gotToken = token
	return f.uid, f.err
}

type fakeUserStore struct {
	user   db.User
	err    error
	calls  int
	gotUID string
}

func (f *fakeUserStore) GetUserByFirebaseUID(_ context.Context, uid string) (db.User, error) {
	f.calls++
	f.gotUID = uid
	return f.user, f.err
}

func TestMiddlewareAttachesActiveUserAndAdmin(t *testing.T) {
	for _, role := range []string{auth.SystemRoleUser, auth.SystemRoleAdmin} {
		t.Run(role, func(t *testing.T) {
			stored := activeDBUser(role)
			verifier := &fakeTokenVerifier{uid: stored.FirebaseUID}
			users := &fakeUserStore{user: stored}
			var got auth.User
			nextCalls := 0
			handler := auth.Middleware(verifier, users)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				var ok bool
				got, ok = auth.FromContext(r.Context())
				if !ok {
					t.Error("authenticated user missing from request context")
				}
				w.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set("Authorization", "Bearer valid-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
			}
			if nextCalls != 1 {
				t.Fatalf("next calls = %d, want 1", nextCalls)
			}
			if verifier.calls != 1 || verifier.gotToken != "valid-token" {
				t.Errorf("verifier calls = %d, token = %q", verifier.calls, verifier.gotToken)
			}
			if users.calls != 1 || users.gotUID != stored.FirebaseUID {
				t.Errorf("store calls = %d, UID = %q", users.calls, users.gotUID)
			}

			want := auth.User{
				ID:          stored.ID,
				FirebaseUID: stored.FirebaseUID,
				Name:        stored.Name,
				Email:       stored.Email,
				UnitID:      stored.UnitID,
				SystemRole:  stored.SystemRole,
			}
			if got != want {
				t.Errorf("context user = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMiddlewareRejectsInvalidToken(t *testing.T) {
	verifier := &fakeTokenVerifier{err: errors.New("firebase provider secret detail")}
	users := &fakeUserStore{user: activeDBUser(auth.SystemRoleUser)}
	nextCalls := 0
	handler := auth.Middleware(verifier, users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}))
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer invalid-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Authentication credentials are invalid or expired.")
	if verifier.calls != 1 || verifier.gotToken != "invalid-token" {
		t.Errorf("verifier calls = %d, token = %q", verifier.calls, verifier.gotToken)
	}
	if users.calls != 0 {
		t.Errorf("store calls = %d, want 0", users.calls)
	}
	if nextCalls != 0 {
		t.Errorf("next calls = %d, want 0", nextCalls)
	}
	if strings.Contains(response.Body.String(), "provider secret") {
		t.Fatalf("private verifier error leaked: %s", response.Body.String())
	}
}

func TestMiddlewareRejectsMalformedAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "wrong scheme", values: []string{"Basic credentials"}},
		{name: "missing token", values: []string{"Bearer"}},
		{name: "extra field", values: []string{"Bearer token extra"}},
		{name: "multiple headers", values: []string{"Bearer first", "Bearer second"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier := &fakeTokenVerifier{uid: "firebase-user-1"}
			users := &fakeUserStore{user: activeDBUser(auth.SystemRoleUser)}
			nextCalls := 0
			handler := auth.Middleware(verifier, users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				nextCalls++
			}))
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			for _, value := range tt.values {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Authentication credentials are required.")
			if verifier.calls != 0 {
				t.Errorf("verifier calls = %d, want 0", verifier.calls)
			}
			if users.calls != 0 {
				t.Errorf("store calls = %d, want 0", users.calls)
			}
			if nextCalls != 0 {
				t.Errorf("next calls = %d, want 0", nextCalls)
			}
		})
	}
}

func TestMiddlewareRejectsUnmappedUID(t *testing.T) {
	verifier := &fakeTokenVerifier{uid: "unmapped-firebase-user"}
	users := &fakeUserStore{err: pgx.ErrNoRows}
	nextCalls := 0
	handler := auth.Middleware(verifier, users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}))
	response := serveAuthorized(handler, "valid-token")

	assertAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Authentication credentials are invalid or expired.")
	if users.calls != 1 || users.gotUID != "unmapped-firebase-user" {
		t.Errorf("store calls = %d, UID = %q", users.calls, users.gotUID)
	}
	if nextCalls != 0 {
		t.Errorf("next calls = %d, want 0", nextCalls)
	}
}

func TestMiddlewareRejectsInactiveUser(t *testing.T) {
	stored := activeDBUser(auth.SystemRoleUser)
	stored.Status = "INACTIVE"
	verifier := &fakeTokenVerifier{uid: stored.FirebaseUID}
	users := &fakeUserStore{user: stored}
	nextCalls := 0
	handler := auth.Middleware(verifier, users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}))
	response := serveAuthorized(handler, "valid-token")

	assertAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized, "Authentication credentials are invalid or expired.")
	if verifier.calls != 1 || users.calls != 1 {
		t.Errorf("verifier calls = %d, store calls = %d; want 1 each", verifier.calls, users.calls)
	}
	if nextCalls != 0 {
		t.Errorf("next calls = %d, want 0", nextCalls)
	}
}

func activeDBUser(role string) db.User {
	return db.User{
		ID:          testUUID(1),
		UnitID:      testUUID(2),
		FirebaseUID: "firebase-user-1",
		Name:        "Sentinel User",
		Email:       "user@example.test",
		Status:      "ACTIVE",
		SystemRole:  role,
	}
}

func testUUID(last byte) pgtype.UUID {
	var id [16]byte
	id[15] = last
	return pgtype.UUID{Bytes: id, Valid: true}
}

func serveAuthorized(handler http.Handler, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code httpapi.ErrorCode, messages ...string) {
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
	if len(messages) > 0 && body.Error.Message != messages[0] {
		t.Errorf("error message = %q, want %q", body.Error.Message, messages[0])
	}
	if body.Error.Details == nil || len(body.Error.Details) != 0 {
		t.Errorf("error details = %#v, want empty object", body.Error.Details)
	}
}
