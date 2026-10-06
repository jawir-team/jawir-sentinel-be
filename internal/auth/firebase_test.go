package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
)

func TestNewVerifierFailsClosedWhenNotConfigured(t *testing.T) {
	verifier := auth.NewVerifier()
	uid, err := verifier.VerifyIDToken(context.Background(), "any-token")

	if uid != "" {
		t.Fatalf("uid = %q, want empty", uid)
	}
	if !errors.Is(err, auth.ErrVerifierNotConfigured) {
		t.Fatalf("error = %v, want ErrVerifierNotConfigured", err)
	}
}
