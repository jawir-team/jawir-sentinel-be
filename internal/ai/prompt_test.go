package ai

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/vertexai"
)

func TestRenderPromptIncludesContractMarkersAndContext(t *testing.T) {
	caseID := testUUID(90)
	policyChunkID := testUUID(91)
	evidenceID := testUUID(92)
	ctx := Context{
		Case: CaseSnapshot{
			ID: caseID, CaseNumber: "CASE-PROMPT-001", CaseTypeID: testUUID(93), Title: "Prompt case",
			Description: "Prompt description", Urgency: "HIGH", Status: "CHECKING",
			CreatedBy: testUUID(94), OwnerID: testUUID(95), CreatedAt: fixedTime, UpdatedAt: fixedTime,
		},
		WorkflowState: "CHECKING",
		PolicyChunks: []PolicyChunkRef{{
			ChunkID: policyChunkID, PolicyID: testUUID(96), PolicyCode: "POL-PROMPT", PolicyTitle: "Prompt Policy",
			PolicyVersionID: testUUID(97), Version: "v3", Section: text("Controls"), ChunkIndex: 2,
			Content: "A reviewer must verify the record.",
		}},
		EvidenceTextParts: []string{"Evidence ID: " + evidenceID.String() + "\nObserved record is incomplete."},
		EvidenceFiles:     []vertexai.FileInput{{URI: "gs://test-bucket/cases/prompt/evidence/report.pdf", MIMEType: "application/pdf"}},
		ReviewerFeedback: []ReviewerFeedbackRef{{
			ActorRole: "CHECKER", Decision: "REJECT", Reason: text("Record missing"), Comment: text("Attach it"), CreatedAt: fixedTime,
		}},
		ExecutionFeedback: &ExecutionFeedbackRef{
			Status: "BLOCKED", ActionTaken: text("Attempted verification"), Result: text("Not verified"),
			Blocker: text("Record unavailable"), StartedAt: fixedTime,
		},
		PreviousAnalysis: &AnalysisSummaryRef{
			Version: 1, Status: "COMPLETED", ModelName: "gemini-test", Summary: text("Prior summary"),
			Recommendation: []byte(`{"summary":"prior"}`), PolicyStatus: text("POLICY_PARTIAL"),
			EvidenceQuality: text("LOW"), Uncertainty: text("HIGH"),
			VerificationStatus: pgtype.Text{String: "PASS_WITH_WARNING", Valid: true}, CreatedAt: fixedTime,
		},
	}

	prompt, err := RenderPrompt(ctx)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if prompt.Version == "" || prompt.Version != PromptVersion {
		t.Fatalf("prompt Version = %q, want non-empty %q", prompt.Version, PromptVersion)
	}
	joined := strings.Join(prompt.TextParts, "\n")
	markers := []string{
		"FACT:", "ASSUMPTION:", "UNKNOWN:", "POLICY:", "REVIEWER_FEEDBACK:", "AI_INFERENCE:",
		"Current Case Snapshot", "Current Workflow State", "Current ACTIVE + READY Policy Chunks",
		"Previous Analysis Summary — REFERENCE ONLY, NOT GROUND TRUTH", "Latest Reviewer Feedback",
		"Latest Execution Feedback", "Observed record is incomplete.", caseID.String(), policyChunkID.String(), evidenceID.String(),
		"The recommendation is an AI recommendation, not a human approval.",
	}
	for _, marker := range markers {
		if !strings.Contains(joined, marker) {
			t.Errorf("rendered prompt does not contain %q", marker)
		}
	}
	if len(prompt.FileParts) != 1 || prompt.FileParts[0] != ctx.EvidenceFiles[0] {
		t.Fatalf("FileParts = %#v, want %#v", prompt.FileParts, ctx.EvidenceFiles)
	}

	request := prompt.ToRequest()
	prompt.TextParts[0] = "mutated"
	prompt.FileParts[0].URI = "mutated"
	if request.TextParts[0] == "mutated" || request.FileParts[0].URI == "mutated" {
		t.Fatal("ToRequest did not defensively copy prompt parts")
	}
}

func TestRenderPromptIsDeterministicAndContainsExactEnumRules(t *testing.T) {
	ctx := Context{Case: CaseSnapshot{ID: testUUID(98)}, WorkflowState: "DRAFT"}
	first, err := RenderPrompt(ctx)
	if err != nil {
		t.Fatalf("first RenderPrompt: %v", err)
	}
	second, err := RenderPrompt(ctx)
	if err != nil {
		t.Fatalf("second RenderPrompt: %v", err)
	}
	if strings.Join(first.TextParts, "\x00") != strings.Join(second.TextParts, "\x00") {
		t.Fatal("RenderPrompt returned different text for identical context")
	}

	joined := strings.Join(first.TextParts, "\n")
	for _, rule := range []string{
		"fact source_type: CASE|EVIDENCE|POLICY",
		"policy_status: POLICY_FOUND|POLICY_PARTIAL|NO_POLICY_FOUND|INSUFFICIENT_EVIDENCE|POLICY_CONFLICT",
		"risk type: OPERATIONAL|COMPLIANCE|FINANCIAL|OTHER",
		"risk level: LOW|MEDIUM|HIGH|CRITICAL",
		"recommendation type: POLICY_BASED|NON_POLICY_RECOMMENDATION",
		"evidence_quality: LOW|MEDIUM|HIGH",
		"uncertainty: LOW|MEDIUM|HIGH",
	} {
		if !strings.Contains(joined, rule) {
			t.Errorf("rendered prompt does not contain exact enum rule %q", rule)
		}
	}
}
