package httpapi

import "net/http"

// Recoverer converts panics into the standard INTERNAL_ERROR response. Panic
// values and stack traces remain private and are never included in the body.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				WriteError(w, nil)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
