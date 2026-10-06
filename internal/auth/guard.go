package auth

import (
	"net/http"

	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

// RequireAuth allows only requests whose identity has been established by
// Middleware. Middleware is responsible for verifying that the mapped user is
// ACTIVE before placing it in the request context.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin allows only the global ADMIN system role. It is intentionally
// independent from case roles and must be composed with all applicable case,
// segregation-of-duties, state, and analysis guards.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := FromContext(r.Context())
		if !ok || user.SystemRole != SystemRoleAdmin {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeForbidden, "", nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}
