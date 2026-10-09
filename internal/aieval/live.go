package aieval

import (
	"context"
	"errors"
	"fmt"

	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/vertexai"
)

// LiveAnalyzer is the shared generation seam used by fixture and Vertex AI
// analyzers. The prompt is already rendered and versioned by package ai.
type LiveAnalyzer interface {
	Generate(context.Context, ai.Prompt) (ai.CandidateAnalysis, ai.VerificationResult, error)
}

// VertexLiveAnalyzer adapts the real Vertex AI client to LiveAnalyzer and uses
// the production parser and verifier for the model response.
type VertexLiveAnalyzer struct {
	client       *vertexai.Client
	verification ai.VerificationContext
}

// NewVertexLiveAnalyzer wires the real Vertex AI adapter for one evaluation
// case. Credential and HTTP-client ownership remain with the caller.
func NewVertexLiveAnalyzer(client *vertexai.Client, verification ai.VerificationContext) (*VertexLiveAnalyzer, error) {
	if client == nil {
		return nil, errors.New("aieval: Vertex AI client is required")
	}
	return &VertexLiveAnalyzer{client: client, verification: verification}, nil
}

// Generate sends the rendered prompt, validates the structured response, and
// verifies it against the case-specific authority boundary.
func (a *VertexLiveAnalyzer) Generate(ctx context.Context, prompt ai.Prompt) (ai.CandidateAnalysis, ai.VerificationResult, error) {
	if a == nil || a.client == nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, errors.New("aieval: Vertex live analyzer is not configured")
	}
	response, err := a.client.GenerateContent(ctx, prompt.ToRequest())
	if err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, fmt.Errorf("aieval: generate candidate: %w", err)
	}
	candidate, err := ai.ParseAndValidateCandidate([]byte(response.Text))
	if err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, fmt.Errorf("aieval: parse candidate: %w", err)
	}
	verifiedCandidate, verification := ai.VerifyCandidate(candidate, a.verification)
	return verifiedCandidate, verification, nil
}

var _ LiveAnalyzer = (*VertexLiveAnalyzer)(nil)
