package auth

import (
	"net/http"

	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

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
