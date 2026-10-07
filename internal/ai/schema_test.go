package ai

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCandidateAnalysisCompleteShape(t *testing.T) {
	candidate := completeCandidate()
	if missing := candidate.MissingRequiredFields(); len(missing) != 0 {
		t.Fatalf("MissingRequiredFields() = %v, want none", missing)
	}
	if !candidate.Complete() {
		t.Fatal("Complete() = false, want true")
	}
	if err := candidate.ValidateEnums(); err != nil {
		t.Fatalf("ValidateEnums() = %v, want nil", err)
	}

	var empty CandidateAnalysis
	wantMissing := []string{
		"summary", "facts", "assumptions", "unknowns", "policy_status", "risk_analysis",
		"compliance_analysis", "recommendation", "alternatives", "missing_information",
		"evidence_quality", "uncertainty",
	}
	if got := empty.MissingRequiredFields(); !slices.Equal(got, wantMissing) {
		t.Fatalf("empty MissingRequiredFields() = %v, want %v", got, wantMissing)
	}
}

func TestCandidateEnumCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		valid func() bool
	}{
		{name: "fact case", valid: func() bool { return FactSourceCase.Valid() }},
		{name: "fact evidence", valid: func() bool { return FactSourceEvidence.Valid() }},
		{name: "fact policy", valid: func() bool { return FactSourcePolicy.Valid() }},
		{name: "fact invalid", valid: func() bool { return FactSourceType("REVIEWER_FEEDBACK").Valid() }},
		{name: "policy found", valid: func() bool { return PolicyStatusFound.Valid() }},
		{name: "policy partial", valid: func() bool { return PolicyStatusPartial.Valid() }},
		{name: "policy not found", valid: func() bool { return PolicyStatusNotFound.Valid() }},
		{name: "policy insufficient", valid: func() bool { return PolicyStatusInsufficientEvidence.Valid() }},
		{name: "policy conflict", valid: func() bool { return PolicyStatusConflict.Valid() }},
		{name: "policy invalid", valid: func() bool { return PolicyStatus("UNKNOWN").Valid() }},
		{name: "risk operational", valid: func() bool { return RiskTypeOperational.Valid() }},
		{name: "risk compliance", valid: func() bool { return RiskTypeCompliance.Valid() }},
		{name: "risk financial", valid: func() bool { return RiskTypeFinancial.Valid() }},
		{name: "risk other", valid: func() bool { return RiskTypeOther.Valid() }},
		{name: "risk type invalid", valid: func() bool { return RiskType("LEGAL").Valid() }},
		{name: "risk low", valid: func() bool { return RiskLevelLow.Valid() }},
		{name: "risk medium", valid: func() bool { return RiskLevelMedium.Valid() }},
		{name: "risk high", valid: func() bool { return RiskLevelHigh.Valid() }},
		{name: "risk critical", valid: func() bool { return RiskLevelCritical.Valid() }},
		{name: "risk level invalid", valid: func() bool { return RiskLevel("SEVERE").Valid() }},
		{name: "recommendation policy", valid: func() bool { return RecommendationTypePolicyBased.Valid() }},
		{name: "recommendation non-policy", valid: func() bool { return RecommendationTypeNonPolicy.Valid() }},
		{name: "recommendation invalid", valid: func() bool { return RecommendationType("APPROVAL").Valid() }},
		{name: "quality low", valid: func() bool { return QualityLow.Valid() }},
		{name: "quality medium", valid: func() bool { return QualityMedium.Valid() }},
		{name: "quality high", valid: func() bool { return QualityHigh.Valid() }},
		{name: "quality invalid", valid: func() bool { return QualityLevel("CRITICAL").Valid() }},
		{name: "compliance no issue", valid: func() bool { return ComplianceNoIssueIdentified.Valid() }},
		{name: "compliance concern", valid: func() bool { return CompliancePotentialConcern.Valid() }},
		{name: "compliance review", valid: func() bool { return ComplianceRequiresReview.Valid() }},
		{name: "compliance invalid", valid: func() bool { return ComplianceStatus("COMPLIANT").Valid() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.valid()
			want := !strings.Contains(tt.name, "invalid")
			if got != want {
				t.Fatalf("Valid() = %t, want %t", got, want)
			}
		})
	}

	candidate := completeCandidate()
	candidate.RiskAnalysis[0].Level = RiskLevel("SEVERE")
	if err := candidate.ValidateEnums(); err == nil || !strings.Contains(err.Error(), "risk_analysis[0].level") {
		t.Fatalf("ValidateEnums() = %v, want risk level error", err)
	}
}

func TestCandidateExplicitUnknownsWithoutPlaceholders(t *testing.T) {
	candidate := completeCandidate()
	candidate.Unknowns = []CandidateUnknown{{
		Item:   "Whether the secondary approval exists",
		Impact: "The policy requirement cannot yet be confirmed",
	}}
	candidate.MissingInformation = []CandidateMissingInformation{{
		Item:      "Secondary approval evidence",
		WhyNeeded: "Needed to assess the approval requirement",
	}}

	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	output := string(encoded)
	for _, want := range []string{"Whether the secondary approval exists", "Secondary approval evidence"} {
		if !strings.Contains(output, want) {
			t.Errorf("candidate JSON does not contain explicit unknown %q: %s", want, output)
		}
	}
	for _, placeholder := range []string{`"N/A"`, `"TBD"`, `"unknown"`} {
		if strings.Contains(output, placeholder) {
			t.Errorf("candidate JSON contains placeholder %s: %s", placeholder, output)
		}
	}
}

func TestCandidateEmptyCollectionsMarshalAsArrays(t *testing.T) {
	candidate := NewCandidateAnalysis()
	candidate.Summary = "No supported alternative was identified."
	candidate.PolicyStatus = PolicyStatusNotFound
	candidate.ComplianceAnalysis.Status = ComplianceRequiresReview
	candidate.Recommendation.Type = RecommendationTypeNonPolicy
	candidate.EvidenceQuality = QualityLow
	candidate.Uncertainty = QualityHigh

	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, field := range []string{"facts", "assumptions", "unknowns", "risk_analysis", "alternatives", "missing_information"} {
		value, ok := shape[field].([]any)
		if !ok || value == nil || len(value) != 0 {
			t.Errorf("%s = %#v, want non-nil empty JSON array", field, shape[field])
		}
	}
	if strings.Contains(string(encoded), `"alternatives":null`) {
		t.Fatalf("alternatives marshaled as null: %s", encoded)
	}
}

func completeCandidate() CandidateAnalysis {
	candidate := NewCandidateAnalysis()
	candidate.Summary = "A supported analysis summary."
	candidate.Facts = []CandidateFact{{
		Statement: "The case is awaiting review.", SourceType: FactSourceCase, SourceRef: "case-id",
	}}
	candidate.PolicyStatus = PolicyStatusPartial
	candidate.RiskAnalysis = []CandidateRisk{{
		Type: RiskTypeOperational, Level: RiskLevelHigh, Reason: "The deadline is near.",
		EvidenceRefs: []string{"evidence-id"}, PolicyRefs: []string{"policy-chunk-id"},
	}}
	candidate.ComplianceAnalysis = &CandidateCompliance{
		Status: ComplianceRequiresReview, Reason: "An approval record is absent.", PolicyRefs: []string{"policy-chunk-id"},
	}
	candidate.Recommendation = &CandidateRecommendation{
		Type: RecommendationTypePolicyBased, Summary: "Verify approval before proceeding.",
		Actions: []CandidateRecommendationAction{{
			Order: 1, Action: "Request the approval record.", Reason: "It is required by policy.",
			PolicyRefs: []string{"policy-chunk-id"}, EvidenceRefs: []string{"evidence-id"},
		}},
		PotentialBenefits: []string{"Reduces compliance risk."},
		PotentialRisks:    []string{"May delay processing."},
	}
	candidate.EvidenceQuality = QualityMedium
	candidate.Uncertainty = QualityMedium
	return candidate
}
