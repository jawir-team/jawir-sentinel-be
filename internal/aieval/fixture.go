package aieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/aieval/evaldata"
)

// FixtureAnalyzer is deterministic and performs no network calls. It derives
// policy status from the scenario and quality from required evidence coverage,
// then delegates grounding verification to the production domain verifier.
type FixtureAnalyzer struct {
	testCase     evaldata.Case
	verification ai.VerificationContext
}

// NewFixtureAnalyzer binds one case to its exact verification authority.
func NewFixtureAnalyzer(testCase evaldata.Case, verification ai.VerificationContext) *FixtureAnalyzer {
	return &FixtureAnalyzer{testCase: testCase, verification: verification}
}

// Generate implements the same seam as a live analyzer.
func (a *FixtureAnalyzer) Generate(ctx context.Context, prompt ai.Prompt) (ai.CandidateAnalysis, ai.VerificationResult, error) {
	if err := ctx.Err(); err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, err
	}
	if a == nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, fmt.Errorf("aieval: fixture analyzer is nil")
	}
	if err := promptContainsFixture(prompt, a.testCase); err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, err
	}
	candidate, err := fixtureCandidate(a.testCase)
	if err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, err
	}
	if err := candidate.Validate(); err != nil {
		return ai.CandidateAnalysis{}, ai.VerificationResult{}, fmt.Errorf("aieval: fixture candidate validation: %w", err)
	}
	verifiedCandidate, result := ai.VerifyCandidate(candidate, a.verification)
	return verifiedCandidate, result, nil
}

func promptContainsFixture(prompt ai.Prompt, testCase evaldata.Case) error {
	if prompt.Version != ai.PromptVersion {
		return fmt.Errorf("aieval: prompt version = %q, want %q", prompt.Version, ai.PromptVersion)
	}
	rendered := strings.Join(prompt.TextParts, "\n")
	for _, required := range []string{testCase.CaseID, testCase.Description} {
		if !strings.Contains(rendered, required) {
			return fmt.Errorf("aieval: rendered prompt does not contain synthetic fixture value %q", required)
		}
	}
	for _, policy := range testCase.PolicyChunks {
		if !strings.Contains(rendered, policy.ID) || !strings.Contains(rendered, policy.Text) {
			return fmt.Errorf("aieval: rendered prompt omits policy chunk %s", policy.ID)
		}
	}
	for _, evidence := range testCase.Evidences {
		if !strings.Contains(rendered, evidence.ID) || !strings.Contains(rendered, evidence.ContentSnippet) {
			return fmt.Errorf("aieval: rendered prompt omits evidence %s", evidence.ID)
		}
	}
	return nil
}

func fixtureCandidate(testCase evaldata.Case) (ai.CandidateAnalysis, error) {
	policyStatus, err := policyStatusForScenario(testCase.Scenario)
	if err != nil {
		return ai.CandidateAnalysis{}, err
	}
	evidenceQuality, uncertainty := qualityFromCoverage(testCase.RequiredEvidenceTypes, testCase.Evidences)
	policyRefs := make([]string, 0, len(testCase.PolicyChunks))
	for _, policy := range testCase.PolicyChunks {
		policyRefs = append(policyRefs, policy.ID)
	}
	evidenceRefs := make([]string, 0, 1)
	if len(testCase.Evidences) > 0 {
		evidenceRefs = append(evidenceRefs, testCase.Evidences[0].ID)
	}

	candidate := ai.NewCandidateAnalysis()
	candidate.Summary = "Synthetic analysis for Bank Nusantara Fiktif: " + testCase.Description
	candidate.Facts = append(candidate.Facts, ai.CandidateFact{
		Statement:  "This is a synthetic Bank Nusantara Fiktif evaluation case.",
		SourceType: ai.FactSourceCase,
		SourceRef:  testCase.CaseID,
	})
	candidate.PolicyStatus = policyStatus
	candidate.EvidenceQuality = evidenceQuality
	candidate.Uncertainty = uncertainty
	candidate.ComplianceAnalysis.Status = complianceForStatus(policyStatus)
	candidate.ComplianceAnalysis.Reason = "The synthetic case requires review against only the supplied fictional context."
	candidate.ComplianceAnalysis.PolicyRefs = append([]string{}, policyRefs...)

	if len(policyRefs) > 0 {
		candidate.Recommendation.Type = ai.RecommendationTypePolicyBased
	} else {
		candidate.Recommendation.Type = ai.RecommendationTypeNonPolicy
	}
	candidate.Recommendation.Summary = "Review the synthetic records before any fictional operational action."
	candidate.Recommendation.Actions = append(candidate.Recommendation.Actions, ai.CandidateRecommendationAction{
		Order:        1,
		Action:       "Review the supplied synthetic case material.",
		Reason:       "The evaluation must remain grounded in the supplied fictional records.",
		PolicyRefs:   append([]string{}, policyRefs...),
		EvidenceRefs: append([]string{}, evidenceRefs...),
	})
	candidate.Recommendation.PotentialBenefits = append(candidate.Recommendation.PotentialBenefits, "Keeps the fictional exercise traceable.")
	candidate.Recommendation.PotentialRisks = append(candidate.Recommendation.PotentialRisks, "Missing synthetic records may require human review.")

	switch testCase.Scenario {
	case evaldata.ScenarioVerifierUnsupportedWarning:
		candidate.RiskAnalysis = append(candidate.RiskAnalysis, ai.CandidateRisk{
			Type: ai.RiskTypeOperational, Level: ai.RiskLevelMedium,
			Reason:       "An intentionally unsupported synthetic risk is emitted for verifier evaluation.",
			EvidenceRefs: []string{}, PolicyRefs: []string{},
		})
	case evaldata.ScenarioVerifierHallucinatedFail:
		candidate.Facts = append(candidate.Facts, ai.CandidateFact{
			Statement:  "An intentionally absent synthetic record was reviewed.",
			SourceType: ai.FactSourceEvidence,
			SourceRef:  "30000000-0000-4000-8000-ffffffffffff",
		})
	case evaldata.ScenarioVerifierContradictionFail:
		candidate.ComplianceAnalysis.Status = ai.ComplianceNoIssueIdentified
		candidate.ComplianceAnalysis.Reason = "Approval without two reviewers is permitted."
	}
	return candidate, nil
}

func policyStatusForScenario(scenario evaldata.ScenarioTag) (ai.PolicyStatus, error) {
	switch scenario {
	case evaldata.ScenarioPolicyFound,
		evaldata.ScenarioVerifierUnsupportedWarning,
		evaldata.ScenarioVerifierHallucinatedFail,
		evaldata.ScenarioVerifierContradictionFail:
		return ai.PolicyStatusFound, nil
	case evaldata.ScenarioPolicyPartial:
		return ai.PolicyStatusPartial, nil
	case evaldata.ScenarioNoPolicyFound:
		return ai.PolicyStatusNotFound, nil
	case evaldata.ScenarioInsufficientEvidence:
		return ai.PolicyStatusInsufficientEvidence, nil
	case evaldata.ScenarioPolicyConflict:
		return ai.PolicyStatusConflict, nil
	default:
		return "", fmt.Errorf("aieval: unknown scenario tag %q", scenario)
	}
}

func qualityFromCoverage(required []string, evidences []evaldata.Evidence) (ai.QualityLevel, ai.QualityLevel) {
	present := make(map[string]struct{}, len(evidences))
	for _, evidence := range evidences {
		present[strings.TrimSpace(evidence.Type)] = struct{}{}
	}
	matched := 0
	seenRequired := make(map[string]struct{}, len(required))
	for _, evidenceType := range required {
		evidenceType = strings.TrimSpace(evidenceType)
		if evidenceType == "" {
			continue
		}
		if _, duplicate := seenRequired[evidenceType]; duplicate {
			continue
		}
		seenRequired[evidenceType] = struct{}{}
		if _, ok := present[evidenceType]; ok {
			matched++
		}
	}
	if len(seenRequired) > 0 && matched == len(seenRequired) {
		return ai.QualityHigh, ai.QualityLow
	}
	if matched > 0 {
		return ai.QualityMedium, ai.QualityMedium
	}
	return ai.QualityLow, ai.QualityHigh
}

func complianceForStatus(status ai.PolicyStatus) ai.ComplianceStatus {
	if status == ai.PolicyStatusFound {
		return ai.ComplianceNoIssueIdentified
	}
	return ai.ComplianceRequiresReview
}

var _ LiveAnalyzer = (*FixtureAnalyzer)(nil)
