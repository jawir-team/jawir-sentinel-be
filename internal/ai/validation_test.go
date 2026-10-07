package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseAndValidateCandidate(t *testing.T) {
	validRaw := marshalCandidate(t, completeCandidate())

	tests := []struct {
		name       string
		raw        []byte
		wantErr    bool
		wantFields []string
	}{
		{
			name: "valid complete JSON",
			raw:  validRaw,
		},
		{
			name:    "malformed JSON",
			raw:     []byte(`{"summary":`),
			wantErr: true,
		},
		{
			name:    "non-object top-level JSON",
			raw:     []byte(`[]`),
			wantErr: true,
		},
		{
			name:    "unknown unsupported field",
			raw:     withJSONField(t, validRaw, "unsupported", true),
			wantErr: true,
		},
		{
			name:       "missing required fields",
			raw:        withoutJSONFields(t, validRaw, "summary", "compliance_analysis"),
			wantErr:    true,
			wantFields: []string{"summary", "compliance_analysis"},
		},
		{
			name:       "null required fields",
			raw:        withJSONFields(t, validRaw, map[string]any{"summary": nil, "facts": nil}),
			wantErr:    true,
			wantFields: []string{"summary", "facts"},
		},
		{
			name:    "trailing JSON value",
			raw:     append(append([]byte{}, validRaw...), []byte(` {}`)...),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAndValidateCandidate(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAndValidateCandidate() error = %v, wantErr %t", err, tt.wantErr)
			}
			for _, field := range tt.wantFields {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("error = %q, want field path %q", err, field)
				}
			}
		})
	}
}

func TestParseAndValidateCandidateAllowsEmptyCollections(t *testing.T) {
	candidate := NewCandidateAnalysis()
	candidate.Summary = "No supported findings."
	candidate.PolicyStatus = PolicyStatusNotFound
	candidate.ComplianceAnalysis.Status = ComplianceRequiresReview
	candidate.ComplianceAnalysis.Reason = "No applicable policy was supplied."
	candidate.Recommendation.Type = RecommendationTypeNonPolicy
	candidate.Recommendation.Summary = "Obtain more information."
	candidate.EvidenceQuality = QualityLow
	candidate.Uncertainty = QualityHigh

	if _, err := ParseAndValidateCandidate(marshalCandidate(t, candidate)); err != nil {
		t.Fatalf("ParseAndValidateCandidate() = %v, want nil", err)
	}
}

func TestCandidateValidateRejectsInvalidEnums(t *testing.T) {
	tests := []struct {
		name  string
		field string
		alter func(*CandidateAnalysis)
	}{
		{"policy status", "policy_status", func(c *CandidateAnalysis) { c.PolicyStatus = "UNKNOWN" }},
		{"fact source type", "facts[0].source_type", func(c *CandidateAnalysis) { c.Facts[0].SourceType = "UNKNOWN" }},
		{"risk type", "risk_analysis[0].type", func(c *CandidateAnalysis) { c.RiskAnalysis[0].Type = "UNKNOWN" }},
		{"risk level", "risk_analysis[0].level", func(c *CandidateAnalysis) { c.RiskAnalysis[0].Level = "UNKNOWN" }},
		{"recommendation type", "recommendation.type", func(c *CandidateAnalysis) { c.Recommendation.Type = "UNKNOWN" }},
		{"compliance status", "compliance_analysis.status", func(c *CandidateAnalysis) { c.ComplianceAnalysis.Status = "UNKNOWN" }},
		{"evidence quality", "evidence_quality", func(c *CandidateAnalysis) { c.EvidenceQuality = "UNKNOWN" }},
		{"uncertainty quality", "uncertainty", func(c *CandidateAnalysis) { c.Uncertainty = "UNKNOWN" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := completeCandidate()
			tt.alter(&candidate)

			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Validate() = %v, want enum error for %s", err, tt.field)
			}
		})
	}
}

func TestCandidateValidateRejectsInvalidNestedShapes(t *testing.T) {
	tests := []struct {
		name  string
		field string
		alter func(*CandidateAnalysis)
	}{
		{
			name: "risk missing reason", field: "risk_analysis[0].reason",
			alter: func(c *CandidateAnalysis) { c.RiskAnalysis[0].Reason = "" },
		},
		{
			name: "risk missing policy refs", field: "risk_analysis[0].policy_refs",
			alter: func(c *CandidateAnalysis) { c.RiskAnalysis[0].PolicyRefs = nil },
		},
		{
			name: "recommendation action missing action", field: "recommendation.actions[0].action",
			alter: func(c *CandidateAnalysis) { c.Recommendation.Actions[0].Action = "" },
		},
		{
			name: "alternative missing summary", field: "alternatives[0].summary",
			alter: func(c *CandidateAnalysis) {
				c.Alternatives = []CandidateAlternative{{Benefits: []string{}, Risks: []string{}}}
			},
		},
		{
			name: "missing information missing why needed", field: "missing_information[0].why_needed",
			alter: func(c *CandidateAnalysis) {
				c.MissingInformation = []CandidateMissingInformation{{Item: "Approval record"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := completeCandidate()
			tt.alter(&candidate)

			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Validate() = %v, want shape error for %s", err, tt.field)
			}
		})
	}

	t.Run("empty nested policy refs are valid", func(t *testing.T) {
		candidate := completeCandidate()
		candidate.RiskAnalysis[0].PolicyRefs = []string{}
		candidate.ComplianceAnalysis.PolicyRefs = []string{}
		candidate.Recommendation.Actions[0].PolicyRefs = []string{}
		if err := candidate.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})
}

func TestCandidateValidateCollectsAllFailures(t *testing.T) {
	candidate := completeCandidate()
	candidate.Facts[0].Statement = ""
	candidate.Facts[0].SourceRef = ""
	candidate.Assumptions = []CandidateAssumption{{}}
	candidate.Unknowns = []CandidateUnknown{{}}
	candidate.RiskAnalysis[0].Reason = ""
	candidate.RiskAnalysis[0].EvidenceRefs = nil
	candidate.ComplianceAnalysis.Reason = ""
	candidate.ComplianceAnalysis.PolicyRefs = nil
	candidate.Recommendation.Summary = ""
	candidate.Recommendation.Actions[0].Reason = ""
	candidate.Recommendation.Actions[0].EvidenceRefs = nil
	candidate.Recommendation.PotentialBenefits = nil
	candidate.Recommendation.PotentialRisks = nil
	candidate.Alternatives = []CandidateAlternative{{}}
	candidate.MissingInformation = []CandidateMissingInformation{{}}

	err := candidate.Validate()
	var validationErrs ValidationErrors
	if !errors.As(err, &validationErrs) {
		t.Fatalf("Validate() error type = %T, want ValidationErrors", err)
	}
	for _, field := range []string{
		"facts[0].statement", "facts[0].source_ref",
		"assumptions[0].statement", "assumptions[0].reason",
		"unknowns[0].item", "unknowns[0].impact",
		"risk_analysis[0].reason", "risk_analysis[0].evidence_refs",
		"compliance_analysis.reason", "compliance_analysis.policy_refs",
		"recommendation.summary", "recommendation.actions[0].reason",
		"recommendation.actions[0].evidence_refs", "recommendation.potential_benefits",
		"recommendation.potential_risks", "alternatives[0].summary",
		"alternatives[0].benefits", "alternatives[0].risks",
		"missing_information[0].item", "missing_information[0].why_needed",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("ValidationErrors.Error() = %q, want field path %q", err, field)
		}
	}
}

func marshalCandidate(t *testing.T, candidate CandidateAnalysis) []byte {
	t.Helper()
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

func decodeJSONObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return object
}

func encodeJSONObject(t *testing.T, object map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

func withJSONField(t *testing.T, raw []byte, field string, value any) []byte {
	t.Helper()
	return withJSONFields(t, raw, map[string]any{field: value})
}

func withJSONFields(t *testing.T, raw []byte, fields map[string]any) []byte {
	t.Helper()
	object := decodeJSONObject(t, raw)
	for field, value := range fields {
		object[field] = value
	}
	return encodeJSONObject(t, object)
}

func withoutJSONFields(t *testing.T, raw []byte, fields ...string) []byte {
	t.Helper()
	object := decodeJSONObject(t, raw)
	for _, field := range fields {
		delete(object, field)
	}
	return encodeJSONObject(t, object)
}
