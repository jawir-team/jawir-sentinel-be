package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

func TestRequireAuthAllowsAuthenticatedUser(t *testing.T) {
	for _, role := range []string{auth.SystemRoleUser, auth.SystemRoleAdmin} {
		t.Run(role, func(t *testing.T) {
			nextCalls := 0
			handler := auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalls++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request = request.WithContext(auth.WithUser(request.Context(), auth.User{SystemRole: role}))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
			if nextCalls != 1 {
				t.Fatalf("next calls = %d, want 1", nextCalls)
			}
		})
	}
}

func TestRequireAuthRejectsMissingAuthenticatedUser(t *testing.T) {
	nextCalls := 0
	handler := auth.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}))
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized)
	if nextCalls != 0 {
		t.Errorf("next calls = %d, want 0", nextCalls)
	}
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	nextCalls := 0
	handler := auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/admin", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{SystemRole: auth.SystemRoleAdmin}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if nextCalls != 1 {
		t.Fatalf("next calls = %d, want 1", nextCalls)
	}
}

func TestRequireAdminDeniesNonAdmin(t *testing.T) {
	for _, tt := range []struct {
		name string
		user *auth.User
	}{
		{name: "user", user: &auth.User{SystemRole: auth.SystemRoleUser}},
		{name: "missing authenticated user"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nextCalls := 0
			handler := auth.RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				nextCalls++
			}))
			request := httptest.NewRequest(http.MethodPost, "/admin", nil)
			if tt.user != nil {
				request = request.WithContext(auth.WithUser(request.Context(), *tt.user))
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
			if nextCalls != 0 {
				t.Errorf("next calls = %d, want 0", nextCalls)
			}
		})
	}
}
