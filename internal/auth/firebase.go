package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
)

var ErrVerifierNotConfigured = errors.New("firebase project ID is required")

type firebaseVerifier struct {
	client *firebaseauth.Client
}

// TokenVerifier verifies a Firebase ID token and returns its Firebase UID.
type TokenVerifier interface {
	VerifyIDToken(ctx context.Context, token string) (uid string, err error)
}

func NewVerifier(ctx context.Context, projectID string) (TokenVerifier, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, ErrVerifierNotConfigured
	}

	configFirebase := &firebase.Config{ProjectID: projectID}

	app, err := firebase.NewApp(ctx, configFirebase)
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase app: %w", err)
	}

	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase auth client: %w", err)
	}

	return &firebaseVerifier{client: client}, nil
}

func (v *firebaseVerifier) VerifyIDToken(ctx context.Context, token string) (string, error) {
	decodedToken, err := v.client.VerifyIDToken(ctx, token)
	if err != nil {
		return "", err
	}
	return decodedToken.UID, nil
}
