package aieval_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/jawir-team/jawir-sentinel-be/internal/aieval"
	"github.com/jawir-team/jawir-sentinel-be/internal/aieval/evaldata"
	"github.com/jawir-team/jawir-sentinel-be/internal/vertexai"
)

func TestLiveDataset(t *testing.T) {
	if os.Getenv("EVAL_LIVE") != "1" {
		t.Skip("set EVAL_LIVE=1 to run the informational Vertex AI evaluation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	config, err := vertexai.ConfigFromEnv()
	if err != nil {
		t.Fatalf("Vertex AI configuration: %v", err)
	}
	tokenSource, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		t.Fatalf("load Application Default Credentials: %v", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		t.Fatalf("obtain Application Default Credentials token: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := vertexai.New(config, token.AccessToken, http.DefaultClient, logger)
	if err != nil {
		t.Fatalf("construct Vertex AI client: %v", err)
	}

	for _, testCase := range evaldata.Cases() {
		t.Run(testCase.Name, func(t *testing.T) {
			prompt, verificationContext, err := aieval.BuildPrompt(ctx, testCase)
			if err != nil {
				t.Fatalf("BuildPrompt() error = %v", err)
			}
			analyzer, err := aieval.NewVertexLiveAnalyzer(client, verificationContext)
			if err != nil {
				t.Fatalf("NewVertexLiveAnalyzer() error = %v", err)
			}
			candidate, verification, err := analyzer.Generate(ctx, prompt)
			if err != nil {
				t.Fatalf("live Generate() error = %v", err)
			}
			if err := aieval.CheckExpected(testCase, candidate, verification); err != nil {
				t.Logf("informational property deviation: %v", err)
			}
		})
	}
}
