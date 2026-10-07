package ai

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

func TestVerifyCandidateStatuses(t *testing.T) {
	tests := []struct {
		name       string
		alter      func(*CandidateAnalysis, *VerificationContext)
		wantStatus VerificationStatus
		wantType   VerifierIssueType
	}{
		{
			name:       "clean candidate passes",
			alter:      func(*CandidateAnalysis, *VerificationContext) {},
			wantStatus: VerificationStatusPass,
		},
		{
			name: "non-critical evidence mismatch warns",
			alter: func(candidate *CandidateAnalysis, ctx *VerificationContext) {
				ctx.KnownEvidenceRefs["supporting-evidence"] = EvidenceReference{
					SupportedClaims: []string{candidate.RiskAnalysis[0].Reason},
				}
			},
			wantStatus: VerificationStatusPassWithWarning,
			wantType:   VerifierIssueEvidenceMismatch,
		},
		{
			name: "unsupported claim warns",
			alter: func(candidate *CandidateAnalysis, _ *VerificationContext) {
				candidate.RiskAnalysis[0].EvidenceRefs = []string{}
				candidate.RiskAnalysis[0].PolicyRefs = []string{}
			},
			wantStatus: VerificationStatusPassWithWarning,
			wantType:   VerifierIssueUnsupportedClaim,
		},
		{
			name: "hallucinated evidence fails",
			alter: func(candidate *CandidateAnalysis, _ *VerificationContext) {
				candidate.RiskAnalysis[0].EvidenceRefs = []string{"invented-evidence"}
			},
			wantStatus: VerificationStatusFail,
			wantType:   VerifierIssueHallucinatedEvidence,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := completeCandidate()
			ctx := cleanVerificationContext(candidate)
			tt.alter(&candidate, &ctx)

			_, result := VerifyCandidate(candidate, ctx)
			if result.Status != tt.wantStatus {
				t.Fatalf("VerifyCandidate() status = %s, want %s; issues = %#v", result.Status, tt.wantStatus, result.Issues)
			}
			if tt.wantType == "" {
				if len(result.Issues) != 0 {
					t.Fatalf("VerifyCandidate() issues = %#v, want none", result.Issues)
				}
				return
			}
			if !hasIssueType(result.Issues, tt.wantType) {
				t.Fatalf("VerifyCandidate() issues = %#v, want type %s", result.Issues, tt.wantType)
			}
		})
	}
}

func TestVerifyCandidatePreservesHallucinatedRelatedRefs(t *testing.T) {
	candidate := completeCandidate()
	candidate.Facts = append(candidate.Facts, CandidateFact{
		Statement:  "An invented policy requires immediate closure.",
		SourceType: FactSourcePolicy,
		SourceRef:  "invented-policy",
	})
	candidate.Recommendation.Actions[0].EvidenceRefs = []string{"invented-evidence", "evidence-id"}

	_, result := VerifyCandidate(candidate, cleanVerificationContext(candidate))
	if result.Status != VerificationStatusFail {
		t.Fatalf("VerifyCandidate() status = %s, want FAIL", result.Status)
	}
	for _, ref := range []string{"invented-policy", "invented-evidence"} {
		if !issueContainsRef(result.Issues, VerifierIssueHallucinatedEvidence, ref) {
			t.Errorf("hallucinated issues = %#v, want related ref %q", result.Issues, ref)
		}
	}
}

func TestVerifyCandidatePolicyContradictionAndConflictAreDataOnly(t *testing.T) {
	candidate := completeCandidate()
	candidate.PolicyStatus = PolicyStatusConflict

	ctx := cleanVerificationContext(candidate)
	policy := ctx.KnownPolicyRefs["policy-chunk-id"]
	policy.ConflictsWith = []string{"policy-conflict-id"}
	ctx.KnownPolicyRefs["policy-chunk-id"] = policy
	ctx.KnownPolicyRefs["policy-conflict-id"] = PolicyReference{
		Content:       "Approval is optional.",
		ConflictsWith: []string{"policy-chunk-id"},
	}

	caseState := workflow.StateAIAnalysis
	_, conflictResult := VerifyCandidate(candidate, ctx)
	if conflictResult.Status != VerificationStatusPassWithWarning {
		t.Fatalf("policy-conflict-only status = %s, want PASS_WITH_WARNING", conflictResult.Status)
	}
	if !hasIssueType(conflictResult.Issues, VerifierIssuePolicyConflict) {
		t.Fatalf("policy-conflict-only issues = %#v, want POLICY_CONFLICT", conflictResult.Issues)
	}
	if caseState != workflow.StateAIAnalysis {
		t.Fatalf("workflow state = %s, want unchanged AI_ANALYSIS", caseState)
	}
	if _, err := workflow.Transition(workflow.StateAIAnalysis, workflow.Event(VerifierIssuePolicyConflict)); !workflow.IsInvalidTransition(err) {
		t.Fatalf("POLICY_CONFLICT transition error = %v, want InvalidTransitionError", err)
	}

	candidate.ComplianceAnalysis.Status = ComplianceNoIssueIdentified
	candidate.ComplianceAnalysis.Reason = "No approval is required."
	policy.ContradictedClaims = []string{"No approval is required."}
	ctx.KnownPolicyRefs["policy-chunk-id"] = policy

	returned, result := VerifyCandidate(candidate, ctx)
	if !reflect.DeepEqual(returned, candidate) {
		t.Fatalf("VerifyCandidate() candidate changed:\n got %#v\nwant %#v", returned, candidate)
	}
	for _, issueType := range []VerifierIssueType{VerifierIssuePolicyContradiction, VerifierIssuePolicyConflict} {
		if !hasIssueType(result.Issues, issueType) {
			t.Errorf("VerifyCandidate() issues = %#v, want %s", result.Issues, issueType)
		}
	}
}

func TestVerificationStatusSeverityRule(t *testing.T) {
	tests := []struct {
		name     string
		issues   []VerifierIssue
		expected VerificationStatus
	}{
		{name: "none", issues: nil, expected: VerificationStatusPass},
		{name: "info only", issues: []VerifierIssue{{Severity: IssueSeverityInfo}}, expected: VerificationStatusPass},
		{name: "warning", issues: []VerifierIssue{{Severity: IssueSeverityWarning}}, expected: VerificationStatusPassWithWarning},
		{
			name: "critical overrides warning",
			issues: []VerifierIssue{
				{Severity: IssueSeverityWarning},
				{Severity: IssueSeverityCritical},
			},
			expected: VerificationStatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusForIssues(tt.issues); got != tt.expected {
				t.Fatalf("statusForIssues() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestVerifyCandidateRecommendationPolicyMismatch(t *testing.T) {
	candidate := completeCandidate()
	candidate.Recommendation.Actions[0].PolicyRefs = []string{"unrelated-policy"}

	ctx := cleanVerificationContext(candidate)
	ctx.KnownPolicyRefs["unrelated-policy"] = PolicyReference{Content: "Archive completed cases."}
	ctx.KnownPolicyRefs["policy-chunk-id"] = PolicyReference{
		SupportedClaims: []string{candidate.Recommendation.Actions[0].Action},
	}

	_, result := VerifyCandidate(candidate, ctx)
	if result.Status != VerificationStatusPassWithWarning {
		t.Fatalf("VerifyCandidate() status = %s, want PASS_WITH_WARNING; issues = %#v", result.Status, result.Issues)
	}
	if !issueContainsRef(result.Issues, VerifierIssueRecommendationPolicyMismatch, "unrelated-policy") {
		t.Fatalf("VerifyCandidate() issues = %#v, want mismatch preserving unrelated-policy", result.Issues)
	}
}

func TestVerifyCandidateMissingCriticalInformation(t *testing.T) {
	candidate := completeCandidate()
	candidate.Unknowns = []CandidateUnknown{{
		Item: "Requester authority", Impact: "Authorization cannot be confirmed.",
	}}
	candidate.MissingInformation = []CandidateMissingInformation{{
		Item: "Approval record", WhyNeeded: "It is required for the policy assessment.",
	}}

	ctx := cleanVerificationContext(candidate)
	ctx.RequiredInformation = []string{"Approval record", "Transaction amount"}
	ctx.AvailableInformation = map[string]struct{}{}

	_, result := VerifyCandidate(candidate, ctx)
	if result.Status != VerificationStatusFail {
		t.Fatalf("VerifyCandidate() status = %s, want FAIL for undeclared required information", result.Status)
	}
	if countIssueType(result.Issues, VerifierIssueMissingCriticalInformation) != 3 {
		t.Fatalf("missing-information issues = %#v, want three deduplicated findings", result.Issues)
	}
}

func TestVerifyCandidateFailPreservesCandidateAndMarshalsNotes(t *testing.T) {
	candidate := completeCandidate()
	candidate.RiskAnalysis[0].EvidenceRefs = []string{"invented-evidence"}
	before := cloneCandidateAnalysis(candidate)

	returned, result := VerifyCandidate(candidate, cleanVerificationContext(candidate))
	if result.Status != VerificationStatusFail {
		t.Fatalf("VerifyCandidate() status = %s, want FAIL", result.Status)
	}
	if !reflect.DeepEqual(candidate, before) {
		t.Fatalf("VerifyCandidate() mutated input:\n got %#v\nwant %#v", candidate, before)
	}
	if !reflect.DeepEqual(returned, before) {
		t.Fatalf("VerifyCandidate() returned candidate changed:\n got %#v\nwant %#v", returned, before)
	}

	notes, err := result.VerificationNotesJSON()
	if err != nil {
		t.Fatalf("VerificationNotesJSON() error = %v", err)
	}
	var decoded []VerifierIssue
	if err := json.Unmarshal(notes, &decoded); err != nil {
		t.Fatalf("verification_notes is not valid issue-array JSON: %v; JSON = %s", err, notes)
	}
	if !reflect.DeepEqual(decoded, result.Issues) {
		t.Fatalf("verification_notes = %#v, want %#v", decoded, result.Issues)
	}
	if len(decoded) == 0 || len(decoded[0].RelatedRefs) == 0 {
		t.Fatalf("verification_notes lost related refs: %#v", decoded)
	}
}

func cleanVerificationContext(candidate CandidateAnalysis) VerificationContext {
	return VerificationContext{
		KnownCaseRefs: map[string]struct{}{
			"case-id": {},
		},
		KnownEvidenceRefs: map[string]EvidenceReference{
			"evidence-id": {},
		},
		KnownPolicyRefs: map[string]PolicyReference{
			"policy-chunk-id": {
				SupportedClaims: []string{candidate.Recommendation.Actions[0].Action},
			},
		},
		AvailableInformation: map[string]struct{}{},
	}
}

func hasIssueType(issues []VerifierIssue, issueType VerifierIssueType) bool {
	return slices.ContainsFunc(issues, func(issue VerifierIssue) bool {
		return issue.Type == issueType
	})
}

func countIssueType(issues []VerifierIssue, issueType VerifierIssueType) int {
	count := 0
	for _, issue := range issues {
		if issue.Type == issueType {
			count++
		}
	}
	return count
}

func issueContainsRef(issues []VerifierIssue, issueType VerifierIssueType, ref string) bool {
	return slices.ContainsFunc(issues, func(issue VerifierIssue) bool {
		return issue.Type == issueType && slices.Contains(issue.RelatedRefs, ref)
	})
}
