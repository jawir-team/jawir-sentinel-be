// Package auth authenticates external identities and exposes the mapped
// Sentinel user to authorization code through the request context.
package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	SystemRoleUser  = "USER"
	SystemRoleAdmin = "ADMIN"
)

// User is the active internal Sentinel identity associated with an
// authenticated Firebase identity. SystemRole is a global role only; it does
// not grant or replace any case role or workflow authorization.
type User struct {
	ID          pgtype.UUID
	FirebaseUID string
	Name        string
	Email       string
	UnitID      pgtype.UUID
	SystemRole  string
}

type userContextKey struct{}

// WithUser returns a context containing the authenticated Sentinel user.
func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// FromContext returns the authenticated Sentinel user, if one is present.
func FromContext(ctx context.Context) (User, bool) {
	if ctx == nil {
		return User{}, false
	}
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}
