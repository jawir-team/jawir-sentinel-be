// Package aieval provides deterministic and live seams for evaluating the AI
// analysis contract with wholly synthetic fixtures.
package aieval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/aieval/evaldata"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

var fixtureTime = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

// Result contains the rendered prompt and the structured outputs assessed by
// an evaluation. It intentionally contains no prose expectation.
type Result struct {
	Prompt       ai.Prompt
	Candidate    ai.CandidateAnalysis
	Verification ai.VerificationResult
}

// BuildPrompt uses the production context-builder seam and prompt renderer to
// turn one synthetic case into the same prompt shape used by Vertex AI.
func BuildPrompt(ctx context.Context, testCase evaldata.Case) (ai.Prompt, ai.VerificationContext, error) {
	queries, err := newFixtureQueries(testCase)
	if err != nil {
		return ai.Prompt{}, ai.VerificationContext{}, err
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builder, err := ai.NewBuilder(queries, "", logger)
	if err != nil {
		return ai.Prompt{}, ai.VerificationContext{}, fmt.Errorf("create context builder: %w", err)
	}
	built, err := builder.Build(ctx, queries.caseRow.ID)
	if err != nil {
		return ai.Prompt{}, ai.VerificationContext{}, fmt.Errorf("build context: %w", err)
	}
	prompt, err := ai.RenderPrompt(built)
	if err != nil {
		return ai.Prompt{}, ai.VerificationContext{}, fmt.Errorf("render prompt: %w", err)
	}
	return prompt, verificationContext(testCase), nil
}

// Evaluate runs one case entirely offline and checks all property expectations.
func Evaluate(ctx context.Context, testCase evaldata.Case) (Result, error) {
	prompt, verification, err := BuildPrompt(ctx, testCase)
	if err != nil {
		return Result{}, err
	}
	analyzer := NewFixtureAnalyzer(testCase, verification)
	candidate, verified, err := analyzer.Generate(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("fixture analysis: %w", err)
	}
	result := Result{Prompt: prompt, Candidate: candidate, Verification: verified}
	if err := CheckExpected(testCase, candidate, verified); err != nil {
		return result, err
	}
	return result, nil
}

// CheckExpected compares structured properties and exact verifier issue pairs.
// Generated summaries, reasons, and recommendations are never compared.
func CheckExpected(testCase evaldata.Case, candidate ai.CandidateAnalysis, verification ai.VerificationResult) error {
	expected := testCase.Expected
	var problems []error
	if candidate.PolicyStatus != expected.PolicyStatus {
		problems = append(problems, fmt.Errorf("policy_status = %s, want %s", candidate.PolicyStatus, expected.PolicyStatus))
	}
	policyRefs := uniquePolicyRefs(candidate)
	if len(policyRefs) < expected.MinPolicyRefs || len(policyRefs) > expected.MaxPolicyRefs {
		problems = append(problems, fmt.Errorf("unique policy refs = %d, want between %d and %d", len(policyRefs), expected.MinPolicyRefs, expected.MaxPolicyRefs))
	}
	if candidate.EvidenceQuality != expected.EvidenceQuality {
		problems = append(problems, fmt.Errorf("evidence_quality = %s, want %s", candidate.EvidenceQuality, expected.EvidenceQuality))
	}
	if candidate.Uncertainty != expected.Uncertainty {
		problems = append(problems, fmt.Errorf("uncertainty = %s, want %s", candidate.Uncertainty, expected.Uncertainty))
	}
	if verification.Status != expected.VerificationStatus {
		problems = append(problems, fmt.Errorf("verification status = %s, want %s", verification.Status, expected.VerificationStatus))
	}
	if err := checkRequiredFields(expected.RequiredNonEmptyFields, candidate); err != nil {
		problems = append(problems, err)
	}
	if err := checkIssues(expected.VerifierIssues, verification.Issues); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func checkRequiredFields(fields []evaldata.CandidateField, candidate ai.CandidateAnalysis) error {
	var missing []error
	for _, field := range fields {
		switch field {
		case evaldata.CandidateFieldSummary:
			if strings.TrimSpace(candidate.Summary) == "" {
				missing = append(missing, fmt.Errorf("required candidate field %s is empty", field))
			}
		case evaldata.CandidateFieldComplianceStatus:
			if candidate.ComplianceAnalysis == nil || candidate.ComplianceAnalysis.Status == "" {
				missing = append(missing, fmt.Errorf("required candidate field %s is empty", field))
			}
		case evaldata.CandidateFieldRecommendationType:
			if candidate.Recommendation == nil || candidate.Recommendation.Type == "" {
				missing = append(missing, fmt.Errorf("required candidate field %s is empty", field))
			}
		default:
			missing = append(missing, fmt.Errorf("unknown required candidate field %q", field))
		}
	}
	return errors.Join(missing...)
}

func checkIssues(expected []evaldata.ExpectedIssue, actual []ai.VerifierIssue) error {
	type issueKey struct {
		issueType ai.VerifierIssueType
		severity  ai.IssueSeverity
	}
	wantCounts := make(map[issueKey]int, len(expected))
	gotCounts := make(map[issueKey]int, len(actual))
	for _, issue := range expected {
		wantCounts[issueKey{issueType: issue.Type, severity: issue.Severity}]++
	}
	for _, issue := range actual {
		gotCounts[issueKey{issueType: issue.Type, severity: issue.Severity}]++
	}
	if equalIssueCounts(wantCounts, gotCounts) {
		return nil
	}
	return fmt.Errorf("verifier issues = %v, want %v", gotCounts, wantCounts)
}

func equalIssueCounts[K comparable](left, right map[K]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if right[key] != count {
			return false
		}
	}
	return true
}

func uniquePolicyRefs(candidate ai.CandidateAnalysis) map[string]struct{} {
	refs := make(map[string]struct{})
	add := func(values []string) {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				refs[value] = struct{}{}
			}
		}
	}
	for _, fact := range candidate.Facts {
		if fact.SourceType == ai.FactSourcePolicy {
			add([]string{fact.SourceRef})
		}
	}
	for _, risk := range candidate.RiskAnalysis {
		add(risk.PolicyRefs)
	}
	if candidate.ComplianceAnalysis != nil {
		add(candidate.ComplianceAnalysis.PolicyRefs)
	}
	if candidate.Recommendation != nil {
		for _, action := range candidate.Recommendation.Actions {
			add(action.PolicyRefs)
		}
	}
	return refs
}

func verificationContext(testCase evaldata.Case) ai.VerificationContext {
	result := ai.VerificationContext{
		KnownCaseRefs:     map[string]struct{}{testCase.CaseID: {}},
		KnownEvidenceRefs: make(map[string]ai.EvidenceReference, len(testCase.Evidences)),
		KnownPolicyRefs:   make(map[string]ai.PolicyReference, len(testCase.PolicyChunks)),
	}
	for _, evidence := range testCase.Evidences {
		result.KnownEvidenceRefs[evidence.ID] = ai.EvidenceReference{Content: evidence.ContentSnippet}
	}
	for _, policy := range testCase.PolicyChunks {
		result.KnownPolicyRefs[policy.ID] = ai.PolicyReference{
			Content:            policy.Text,
			ContradictedClaims: append([]string(nil), policy.ContradictedClaims...),
			ConflictsWith:      append([]string(nil), policy.ConflictsWith...),
		}
	}
	return result
}

type fixtureQueries struct {
	caseRow      db.Case
	evidences    []db.CaseEvidence
	policyChunks []db.ListActiveReadyPolicyChunksRow
}

func newFixtureQueries(testCase evaldata.Case) (*fixtureQueries, error) {
	caseID, err := parseUUID(testCase.CaseID)
	if err != nil {
		return nil, fmt.Errorf("case %q has invalid case ID: %w", testCase.Name, err)
	}
	caseTypeID := deterministicUUID(caseID, 0x31)
	ownerID := deterministicUUID(caseID, 0x41)
	queries := &fixtureQueries{
		caseRow: db.Case{
			ID:          caseID,
			CaseNumber:  "BNF-SYNTHETIC-" + strings.ToUpper(string(testCase.Scenario)),
			CaseTypeID:  caseTypeID,
			Title:       "Bank Nusantara Fiktif synthetic evaluation",
			Description: testCase.Description,
			Urgency:     "NORMAL",
			Status:      "AI_ANALYSIS",
			CreatedBy:   ownerID,
			OwnerID:     ownerID,
			CreatedAt:   fixtureTime,
			UpdatedAt:   fixtureTime,
		},
		evidences:    make([]db.CaseEvidence, 0, len(testCase.Evidences)),
		policyChunks: make([]db.ListActiveReadyPolicyChunksRow, 0, len(testCase.PolicyChunks)),
	}
	for _, evidence := range testCase.Evidences {
		id, err := parseUUID(evidence.ID)
		if err != nil {
			return nil, fmt.Errorf("case %q has invalid evidence ID %q: %w", testCase.Name, evidence.ID, err)
		}
		queries.evidences = append(queries.evidences, db.CaseEvidence{
			ID:           id,
			CaseID:       caseID,
			EvidenceType: evidence.Type,
			SourceType:   "SYNTHETIC_FIXTURE",
			Title:        pgtype.Text{String: evidence.Type + " fixture", Valid: true},
			Content:      pgtype.Text{String: evidence.ContentSnippet, Valid: true},
			CreatedAt:    fixtureTime,
		})
	}
	for index, policy := range testCase.PolicyChunks {
		id, err := parseUUID(policy.ID)
		if err != nil {
			return nil, fmt.Errorf("case %q has invalid policy chunk ID %q: %w", testCase.Name, policy.ID, err)
		}
		queries.policyChunks = append(queries.policyChunks, db.ListActiveReadyPolicyChunksRow{
			ChunkID:         id,
			PolicyVersionID: deterministicUUID(id, 0x51),
			ChunkIndex:      int32(index),
			Section:         pgtype.Text{String: "Synthetic evaluation", Valid: true},
			Content:         policy.Text,
			Version:         "synthetic-v1",
			PolicyID:        deterministicUUID(id, 0x61),
			PolicyCode:      fmt.Sprintf("BNF-SYN-%02d", index+1),
			PolicyTitle:     "Bank Nusantara Fiktif Synthetic Policy",
		})
	}
	return queries, nil
}

func (q *fixtureQueries) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	return q.caseRow, nil
}

func (q *fixtureQueries) ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error) {
	return append([]db.CaseEvidence(nil), q.evidences...), nil
}

func (q *fixtureQueries) ListActiveReadyPolicyChunks(context.Context, pgtype.UUID) ([]db.ListActiveReadyPolicyChunksRow, error) {
	return append([]db.ListActiveReadyPolicyChunksRow(nil), q.policyChunks...), nil
}

func (*fixtureQueries) GetLatestAnalysisForCase(context.Context, pgtype.UUID) (db.AiAnalysis, error) {
	return db.AiAnalysis{}, pgx.ErrNoRows
}

func (*fixtureQueries) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) {
	return nil, nil
}

func (*fixtureQueries) ListExecutionsForCase(context.Context, pgtype.UUID) ([]db.Execution, error) {
	return nil, nil
}

func parseUUID(raw string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func deterministicUUID(source pgtype.UUID, marker byte) pgtype.UUID {
	result := source
	result.Bytes[0] = marker
	result.Bytes[6] = (result.Bytes[6] & 0x0f) | 0x40
	result.Bytes[8] = (result.Bytes[8] & 0x3f) | 0x80
	result.Valid = true
	return result
}

var _ ai.ContextQueries = (*fixtureQueries)(nil)
