package auth_test

import (
	"context"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
)

func TestUserContextRoundTrip(t *testing.T) {
	want := auth.User{
		ID:          testUUID(1),
		FirebaseUID: "firebase-user-1",
		Name:        "Sentinel User",
		Email:       "user@example.test",
		UnitID:      testUUID(2),
		SystemRole:  auth.SystemRoleUser,
	}

	got, ok := auth.FromContext(auth.WithUser(context.Background(), want))
	if !ok {
		t.Fatal("FromContext did not find the stored user")
	}
	if got != want {
		t.Fatalf("FromContext user = %+v, want %+v", got, want)
	}
}

func TestFromContextWithoutUser(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "background context", ctx: context.Background()},
		{name: "nil context", ctx: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user, ok := auth.FromContext(tt.ctx)
			if ok {
				t.Fatalf("FromContext unexpectedly found user %+v", user)
			}
			if user != (auth.User{}) {
				t.Fatalf("FromContext user = %+v, want zero value", user)
			}
		})
	}
}
