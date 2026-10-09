package aieval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/aieval"
	"github.com/jawir-team/jawir-sentinel-be/internal/aieval/evaldata"
)

func TestSyntheticDataset(t *testing.T) {
	cases := evaldata.Cases()
	if len(cases) == 0 {
		t.Fatal("synthetic evaluation dataset is empty")
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			t.Parallel()
			result, err := aieval.Evaluate(context.Background(), testCase)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Prompt.Version != ai.PromptVersion {
				t.Errorf("prompt version = %q, want %q", result.Prompt.Version, ai.PromptVersion)
			}
			promptText := strings.Join(result.Prompt.TextParts, "\n")
			if !strings.Contains(promptText, "Bank Nusantara Fiktif") {
				t.Error("rendered prompt does not identify the fictional bank")
			}
			if err := result.Candidate.Validate(); err != nil {
				t.Errorf("fixture candidate is not schema-valid: %v", err)
			}
		})
	}
}

func TestDatasetCoversRequiredScenarios(t *testing.T) {
	want := map[evaldata.ScenarioTag]bool{
		evaldata.ScenarioPolicyFound:                false,
		evaldata.ScenarioPolicyPartial:              false,
		evaldata.ScenarioNoPolicyFound:              false,
		evaldata.ScenarioInsufficientEvidence:       false,
		evaldata.ScenarioPolicyConflict:             false,
		evaldata.ScenarioVerifierUnsupportedWarning: false,
		evaldata.ScenarioVerifierHallucinatedFail:   false,
		evaldata.ScenarioVerifierContradictionFail:  false,
	}
	for _, testCase := range evaldata.Cases() {
		if _, required := want[testCase.Scenario]; required {
			want[testCase.Scenario] = true
		}
	}
	for scenario, covered := range want {
		if !covered {
			t.Errorf("required scenario %q is not covered", scenario)
		}
	}
}

func TestCasesReturnsDefensiveCopy(t *testing.T) {
	first := evaldata.Cases()
	first[0].PolicyChunks[0].Text = "mutated"
	first[0].Expected.RequiredNonEmptyFields[0] = "mutated"
	second := evaldata.Cases()
	if second[0].PolicyChunks[0].Text == "mutated" || second[0].Expected.RequiredNonEmptyFields[0] == "mutated" {
		t.Fatal("Cases() leaked mutable canonical fixture data")
	}
}
