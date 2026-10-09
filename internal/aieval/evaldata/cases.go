// Package evaldata contains the synthetic, version-controlled AI evaluation
// cases. The fixtures describe only the fictional Bank Nusantara Fiktif.
package evaldata

import (
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
)

// ScenarioTag selects the deterministic behavior exercised by a fixture.
type ScenarioTag string

const (
	ScenarioPolicyFound                ScenarioTag = "policy_found"
	ScenarioPolicyPartial              ScenarioTag = "policy_partial"
	ScenarioNoPolicyFound              ScenarioTag = "no_policy_found"
	ScenarioInsufficientEvidence       ScenarioTag = "insufficient_evidence"
	ScenarioPolicyConflict             ScenarioTag = "policy_conflict"
	ScenarioVerifierUnsupportedWarning ScenarioTag = "verifier_unsupported_warning"
	ScenarioVerifierHallucinatedFail   ScenarioTag = "verifier_hallucinated_fail"
	ScenarioVerifierContradictionFail  ScenarioTag = "verifier_contradiction_fail"
)

// CandidateField identifies a generated field that must contain a value.
type CandidateField string

const (
	CandidateFieldSummary            CandidateField = "summary"
	CandidateFieldComplianceStatus   CandidateField = "compliance_analysis.status"
	CandidateFieldRecommendationType CandidateField = "recommendation.type"
)

// PolicyChunk is a synthetic policy excerpt. Semantic annotations are used by
// the deterministic verifier and are not sent as separate prompt instructions.
type PolicyChunk struct {
	ID                 string
	Text               string
	ConflictsWith      []string
	ContradictedClaims []string
}

// Evidence is a synthetic text fixture representing a file-content snippet.
// Type participates in deterministic evidence-coverage evaluation.
type Evidence struct {
	ID             string
	Type           string
	ContentSnippet string
}

// ExpectedIssue is an issue type/severity pair expected from verification.
type ExpectedIssue struct {
	Type     ai.VerifierIssueType
	Severity ai.IssueSeverity
}

// ExpectedProperties deliberately excludes generated prose.
type ExpectedProperties struct {
	PolicyStatus           ai.PolicyStatus
	MinPolicyRefs          int
	MaxPolicyRefs          int
	EvidenceQuality        ai.QualityLevel
	Uncertainty            ai.QualityLevel
	VerificationStatus     ai.VerificationStatus
	VerifierIssues         []ExpectedIssue
	RequiredNonEmptyFields []CandidateField
}

// Case is one fully synthetic evaluation fixture.
type Case struct {
	Name                  string
	Description           string
	Scenario              ScenarioTag
	CaseID                string
	PolicyChunks          []PolicyChunk
	Evidences             []Evidence
	RequiredEvidenceTypes []string
	Expected              ExpectedProperties
}

var requiredCandidateFields = []CandidateField{
	CandidateFieldSummary,
	CandidateFieldComplianceStatus,
	CandidateFieldRecommendationType,
}

// Cases returns fresh fixture slices so callers cannot mutate the canonical
// dataset shared by other tests.
func Cases() []Case {
	cases := []Case{
		{
			Name:        "policy_found_complete_evidence",
			Description: "A fictional branch request has a directly applicable approval policy and all required records.",
			Scenario:    ScenarioPolicyFound,
			CaseID:      "10000000-0000-4000-8000-000000000001",
			PolicyChunks: []PolicyChunk{{
				ID:   "20000000-0000-4000-8000-000000000001",
				Text: "Bank Nusantara Fiktif policy BNF-OPS-01 requires a request record and an approval log before a branch equipment replacement.",
			}},
			Evidences: []Evidence{
				{ID: "30000000-0000-4000-8000-000000000001", Type: "REQUEST_RECORD", ContentSnippet: "Synthetic request BNF-FIKTIF-001 asks to replace the training-room document scanner."},
				{ID: "30000000-0000-4000-8000-000000000002", Type: "APPROVAL_LOG", ContentSnippet: "Synthetic approval log records review by the fictional branch operations role."},
			},
			RequiredEvidenceTypes: []string{"REQUEST_RECORD", "APPROVAL_LOG"},
			Expected:              expected(ai.PolicyStatusFound, 1, 1, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusPass),
		},
		{
			Name:        "policy_partial_missing_record",
			Description: "The supplied fictional policy addresses approval but not the unresolved asset-inspection step.",
			Scenario:    ScenarioPolicyPartial,
			CaseID:      "10000000-0000-4000-8000-000000000002",
			PolicyChunks: []PolicyChunk{{
				ID:   "20000000-0000-4000-8000-000000000002",
				Text: "Bank Nusantara Fiktif policy BNF-OPS-02 requires an approval log for training-room equipment service.",
			}},
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000003", Type: "APPROVAL_LOG", ContentSnippet: "Synthetic approval log permits inspection of the fictional training-room scanner.",
			}},
			RequiredEvidenceTypes: []string{"APPROVAL_LOG", "INSPECTION_REPORT"},
			Expected:              expected(ai.PolicyStatusPartial, 1, 1, ai.QualityMedium, ai.QualityMedium, ai.VerificationStatusPass),
		},
		{
			Name:        "no_policy_found_supported_case",
			Description: "The fictional archive request is documented, but no active policy chunk addresses it.",
			Scenario:    ScenarioNoPolicyFound,
			CaseID:      "10000000-0000-4000-8000-000000000003",
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000004", Type: "ARCHIVE_REQUEST", ContentSnippet: "Synthetic request asks where to store a retired fictional training poster.",
			}},
			RequiredEvidenceTypes: []string{"ARCHIVE_REQUEST"},
			Expected: expected(ai.PolicyStatusNotFound, 0, 0, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusPassWithWarning,
				ExpectedIssue{Type: ai.VerifierIssueUnsupportedClaim, Severity: ai.IssueSeverityWarning}),
		},
		{
			Name:        "insufficient_evidence",
			Description: "A fictional request lacks the authorization and request records needed for analysis.",
			Scenario:    ScenarioInsufficientEvidence,
			CaseID:      "10000000-0000-4000-8000-000000000004",
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000005", Type: "GENERAL_NOTE", ContentSnippet: "Synthetic note says only that a training-room task may be needed.",
			}},
			RequiredEvidenceTypes: []string{"REQUEST_RECORD", "AUTHORIZATION_RECORD"},
			Expected: expected(ai.PolicyStatusInsufficientEvidence, 0, 0, ai.QualityLow, ai.QualityHigh, ai.VerificationStatusPassWithWarning,
				ExpectedIssue{Type: ai.VerifierIssueUnsupportedClaim, Severity: ai.IssueSeverityWarning}),
		},
		{
			Name:        "policy_conflict",
			Description: "Two current fictional policy chunks prescribe incompatible reviewer counts for the same exercise.",
			Scenario:    ScenarioPolicyConflict,
			CaseID:      "10000000-0000-4000-8000-000000000005",
			PolicyChunks: []PolicyChunk{
				{
					ID: "20000000-0000-4000-8000-000000000003", Text: "Bank Nusantara Fiktif policy BNF-LAB-03 requires one reviewer for the tabletop exercise.",
					ConflictsWith: []string{"20000000-0000-4000-8000-000000000004"},
				},
				{
					ID: "20000000-0000-4000-8000-000000000004", Text: "Bank Nusantara Fiktif policy BNF-LAB-04 requires two reviewers for the same tabletop exercise.",
					ConflictsWith: []string{"20000000-0000-4000-8000-000000000003"},
				},
			},
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000006", Type: "EXERCISE_REQUEST", ContentSnippet: "Synthetic request schedules the fictional tabletop exercise.",
			}},
			RequiredEvidenceTypes: []string{"EXERCISE_REQUEST"},
			Expected: expected(ai.PolicyStatusConflict, 2, 2, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusPassWithWarning,
				ExpectedIssue{Type: ai.VerifierIssuePolicyConflict, Severity: ai.IssueSeverityWarning}),
		},
		{
			Name:        "verifier_warns_on_unsupported_grounding",
			Description: "The fixture analyzer emits one synthetic risk without evidence or policy references.",
			Scenario:    ScenarioVerifierUnsupportedWarning,
			CaseID:      "10000000-0000-4000-8000-000000000006",
			PolicyChunks: []PolicyChunk{{
				ID: "20000000-0000-4000-8000-000000000005", Text: "Bank Nusantara Fiktif policy BNF-LAB-05 permits a labelled demonstration after an exercise request is recorded.",
			}},
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000007", Type: "EXERCISE_REQUEST", ContentSnippet: "Synthetic record requests a labelled demonstration in the fictional learning lab.",
			}},
			RequiredEvidenceTypes: []string{"EXERCISE_REQUEST"},
			Expected: expected(ai.PolicyStatusFound, 1, 1, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusPassWithWarning,
				ExpectedIssue{Type: ai.VerifierIssueUnsupportedClaim, Severity: ai.IssueSeverityWarning}),
		},
		{
			Name:        "verifier_fails_hallucinated_evidence",
			Description: "The fixture analyzer cites an evidence identifier that the synthetic context never supplied.",
			Scenario:    ScenarioVerifierHallucinatedFail,
			CaseID:      "10000000-0000-4000-8000-000000000007",
			PolicyChunks: []PolicyChunk{{
				ID: "20000000-0000-4000-8000-000000000006", Text: "Bank Nusantara Fiktif policy BNF-LAB-06 requires a checklist for a fictional learning-lab demonstration.",
			}},
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000008", Type: "CHECKLIST", ContentSnippet: "Synthetic checklist marks the fictional demonstration area as prepared.",
			}},
			RequiredEvidenceTypes: []string{"CHECKLIST"},
			Expected: expected(ai.PolicyStatusFound, 1, 1, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusFail,
				ExpectedIssue{Type: ai.VerifierIssueHallucinatedEvidence, Severity: ai.IssueSeverityCritical}),
		},
		{
			Name:        "verifier_fails_policy_verdict_contradiction",
			Description: "The fixture analyzer returns a compliance verdict explicitly contradicted by the cited fictional policy.",
			Scenario:    ScenarioVerifierContradictionFail,
			CaseID:      "10000000-0000-4000-8000-000000000008",
			PolicyChunks: []PolicyChunk{{
				ID:                 "20000000-0000-4000-8000-000000000007",
				Text:               "Bank Nusantara Fiktif policy BNF-LAB-07 prohibits approval without two reviewers.",
				ContradictedClaims: []string{"approval without two reviewers is permitted"},
			}},
			Evidences: []Evidence{{
				ID: "30000000-0000-4000-8000-000000000009", Type: "REVIEW_LOG", ContentSnippet: "Synthetic review log records only one reviewer for the fictional exercise.",
			}},
			RequiredEvidenceTypes: []string{"REVIEW_LOG"},
			Expected: expected(ai.PolicyStatusFound, 1, 1, ai.QualityHigh, ai.QualityLow, ai.VerificationStatusFail,
				ExpectedIssue{Type: ai.VerifierIssuePolicyContradiction, Severity: ai.IssueSeverityCritical}),
		},
	}

	return cloneCases(cases)
}

func expected(
	policyStatus ai.PolicyStatus,
	minPolicyRefs, maxPolicyRefs int,
	evidenceQuality, uncertainty ai.QualityLevel,
	verificationStatus ai.VerificationStatus,
	issues ...ExpectedIssue,
) ExpectedProperties {
	return ExpectedProperties{
		PolicyStatus:           policyStatus,
		MinPolicyRefs:          minPolicyRefs,
		MaxPolicyRefs:          maxPolicyRefs,
		EvidenceQuality:        evidenceQuality,
		Uncertainty:            uncertainty,
		VerificationStatus:     verificationStatus,
		VerifierIssues:         issues,
		RequiredNonEmptyFields: append([]CandidateField(nil), requiredCandidateFields...),
	}
}

func cloneCases(source []Case) []Case {
	result := make([]Case, len(source))
	for i, item := range source {
		result[i] = item
		result[i].PolicyChunks = append([]PolicyChunk(nil), item.PolicyChunks...)
		for j := range result[i].PolicyChunks {
			result[i].PolicyChunks[j].ConflictsWith = append([]string(nil), item.PolicyChunks[j].ConflictsWith...)
			result[i].PolicyChunks[j].ContradictedClaims = append([]string(nil), item.PolicyChunks[j].ContradictedClaims...)
		}
		result[i].Evidences = append([]Evidence(nil), item.Evidences...)
		result[i].RequiredEvidenceTypes = append([]string(nil), item.RequiredEvidenceTypes...)
		result[i].Expected.VerifierIssues = append([]ExpectedIssue(nil), item.Expected.VerifierIssues...)
		result[i].Expected.RequiredNonEmptyFields = append([]CandidateField(nil), item.Expected.RequiredNonEmptyFields...)
	}
	return result
}
