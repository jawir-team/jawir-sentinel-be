package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
)

func TestNewVerifierFailsClosedWhenNotConfigured(t *testing.T) {
	verifier, err := auth.NewVerifier(context.Background(), " ")
	if verifier != nil {
		t.Fatalf("verifier = %T, want nil", verifier)
	}
	if !errors.Is(err, auth.ErrVerifierNotConfigured) {
		t.Fatalf("error = %v, want ErrVerifierNotConfigured", err)
	}
}
