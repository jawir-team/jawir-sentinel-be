package storage

import (
	"strings"
	"testing"
	"time"
)

func TestNewFromEnvReturnsNilWithoutBucket(t *testing.T) {
	t.Setenv("GCS_BUCKET", "   ")
	if store := NewFromEnv(); store != nil {
		t.Fatalf("NewFromEnv() = %T, want nil when GCS_BUCKET is empty", store)
	}
}

func TestSignedURLTTL(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "default", raw: "", want: 900 * time.Second},
		{name: "invalid uses default", raw: "not-a-number", want: 900 * time.Second},
		{name: "minimum", raw: "1", want: 60 * time.Second},
		{name: "configured", raw: "1200", want: 1200 * time.Second},
		{name: "maximum", raw: "7200", want: 3600 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GCS_SIGNED_URL_TTL_SECONDS", tt.raw)
			if got := signedURLTTL(); got != tt.want {
				t.Errorf("signedURLTTL() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSignUploadURLRequiresPrivateKeyCredentials(t *testing.T) {
	store := &gcsStore{
		bucket:      "evidence-bucket",
		credentials: serviceAccountCredentials{ClientEmail: "signer@example.test"},
		ttl:         15 * time.Minute,
	}

	_, err := store.SignUploadURL(t.Context(), "cases/id/evidence/file.pdf", "application/pdf")
	if err == nil || !strings.Contains(err.Error(), "private_key") {
		t.Fatalf("SignUploadURL() error = %v, want descriptive missing private_key error", err)
	}
}
