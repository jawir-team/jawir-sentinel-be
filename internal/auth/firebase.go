package auth

import (
	"context"
	"errors"
)

// ErrVerifierNotConfigured is returned by the local verifier placeholder.
// Replace NewVerifier with a firebase-admin-go-backed implementation when the
// runtime has Firebase project credentials.
var ErrVerifierNotConfigured = errors.New("firebase token verifier is not configured")

// TokenVerifier verifies a Firebase ID token and returns its Firebase UID.
type TokenVerifier interface {
	VerifyIDToken(ctx context.Context, token string) (uid string, err error)
}

type unconfiguredVerifier struct{}

// NewVerifier returns a verifier that fails closed with
// ErrVerifierNotConfigured. No Firebase credentials or Admin SDK integration
// are available in this repository yet; tests inject a verifier at this
// interface boundary.
func NewVerifier() TokenVerifier {
	return unconfiguredVerifier{}
}

func (unconfiguredVerifier) VerifyIDToken(context.Context, string) (string, error) {
	return "", ErrVerifierNotConfigured
}
