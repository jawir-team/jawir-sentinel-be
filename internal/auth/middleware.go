package auth

import (
	"context"
	"net/http"
	"strings"

	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

const activeUserStatus = "ACTIVE"

// UserStore is the database boundary needed to map a Firebase identity to a
// Sentinel user. *db.Queries satisfies this interface.
type UserStore interface {
	GetUserByFirebaseUID(ctx context.Context, firebaseUID string) (db.User, error)
}

// Middleware authenticates a Firebase bearer token, maps it to an ACTIVE
// Sentinel user, and attaches that user to the request context. Authentication
// failures deliberately share one public response so callers cannot enumerate
// mapped UIDs or user status.
func Middleware(verifier TokenVerifier, users UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r.Header.Values("Authorization"))
			if !ok || verifier == nil || users == nil {
				writeUnauthorized(w)
				return
			}

			firebaseUID, err := verifier.VerifyIDToken(r.Context(), token)
			if err != nil || firebaseUID == "" {
				writeUnauthorized(w)
				return
			}

			storedUser, err := users.GetUserByFirebaseUID(r.Context(), firebaseUID)
			if err != nil || !validStoredUser(storedUser, firebaseUID) {
				writeUnauthorized(w)
				return
			}

			user := User{
				ID:          storedUser.ID,
				FirebaseUID: storedUser.FirebaseUID,
				Name:        storedUser.Name,
				Email:       storedUser.Email,
				UnitID:      storedUser.UnitID,
				SystemRole:  storedUser.SystemRole,
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func validStoredUser(user db.User, verifiedUID string) bool {
	if user.Status != activeUserStatus || user.FirebaseUID != verifiedUID {
		return false
	}
	if !user.ID.Valid || !user.UnitID.Valid {
		return false
	}
	return user.SystemRole == SystemRoleUser || user.SystemRole == SystemRoleAdmin
}

func writeUnauthorized(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
}
