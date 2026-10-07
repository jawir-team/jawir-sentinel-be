package analysisrepo

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

func TestAnalysisProvenancePersistence(t *testing.T) {
	pool, ctx := openTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	service := New(tx)

	t.Run("completed analysis persists multiple exact refs and distinct usages", func(t *testing.T) {
		const seed = 20
		caseID := insertCaseFixture(t, ctx, tx, seed)
		policyID := insertPolicyFixture(t, ctx, tx, seed, caseID)
		v1 := insertPolicyVersionFixture(t, ctx, tx, seed, 1, policyID, "DRAFT")
		v2 := insertPolicyVersionFixture(t, ctx, tx, seed, 2, policyID, "DRAFT")
		evidenceID := insertEvidenceFixture(t, ctx, tx, seed, 1, caseID)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		provenance := &ai.AnalysisProvenance{
			PolicyRefs: []ai.PolicyRef{
				policyRef(v1, "Limits", "Approval limit v1", 87500),
				policyRef(v2, "Exceptions", "Exception rule v2", 62500),
			},
			EvidenceRefs: []ai.EvidenceRef{
				{EvidenceID: evidenceID, UsageType: ai.EvidenceUsageSupportingFact},
				{EvidenceID: evidenceID, UsageType: ai.EvidenceUsageContext},
			},
		}
		if _, err := service.FinalizeCompleted(ctx, attempt.ID, attempt.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, provenance); err != nil {
			t.Fatal(err)
		}

		policyRefs, err := service.PolicyRefs(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(policyRefs) != 2 {
			t.Fatalf("policy refs = %+v, want 2", policyRefs)
		}
		byVersion := make(map[[16]byte]db.AnalysisPolicyRef, len(policyRefs))
		for _, ref := range policyRefs {
			if ref.AnalysisID != attempt.ID {
				t.Errorf("analysis ID = %v, want %v", ref.AnalysisID, attempt.ID)
			}
			byVersion[ref.PolicyVersionID.Bytes] = ref
		}
		assertStoredPolicyRef(t, byVersion[v1.Bytes], "Limits", "Approval limit v1", 0.875)
		assertStoredPolicyRef(t, byVersion[v2.Bytes], "Exceptions", "Exception rule v2", 0.625)

		evidenceRefs, err := service.EvidenceRefs(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evidenceRefs) != 2 {
			t.Fatalf("evidence refs = %+v, want 2", evidenceRefs)
		}
		usages := map[string]bool{}
		for _, ref := range evidenceRefs {
			if ref.AnalysisID != attempt.ID || ref.EvidenceID != evidenceID {
				t.Errorf("evidence ref = %+v", ref)
			}
			usages[ref.UsageType] = true
		}
		if !usages[string(ai.EvidenceUsageSupportingFact)] || !usages[string(ai.EvidenceUsageContext)] {
			t.Errorf("usage types = %v", usages)
		}
	})

	t.Run("duplicate exact evidence usage rolls back finalize", func(t *testing.T) {
		const seed = 21
		caseID := insertCaseFixture(t, ctx, tx, seed)
		evidenceID := insertEvidenceFixture(t, ctx, tx, seed, 1, caseID)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		ref := ai.EvidenceRef{EvidenceID: evidenceID, UsageType: ai.EvidenceUsageContext}
		_, err = service.FinalizeCompleted(
			ctx, attempt.ID, attempt.WorkerAttemptID, validCandidate(),
			ai.VerificationResult{Status: ai.VerificationStatusPass},
			&ai.AnalysisProvenance{EvidenceRefs: []ai.EvidenceRef{ref, ref}},
		)
		if !errors.Is(err, ai.ErrDuplicateEvidenceRef) || !errors.Is(err, ai.ErrInvalidProvenance) {
			t.Fatalf("error = %v, want duplicate provenance error", err)
		}
		stored, err := db.New(tx).GetAnalysis(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != "GENERATING" {
			t.Fatalf("status = %q, want GENERATING", stored.Status)
		}
		assertNoStoredRefs(t, ctx, service, attempt.ID)
	})

	t.Run("historical refs remain on old policy version", func(t *testing.T) {
		const seed = 22
		caseID := insertCaseFixture(t, ctx, tx, seed)
		policyID := insertPolicyFixture(t, ctx, tx, seed, caseID)
		v1 := insertPolicyVersionFixture(t, ctx, tx, seed, 1, policyID, "DRAFT")
		if _, err := tx.Exec(ctx, `
			UPDATE policy_versions
			SET status = 'ACTIVE', index_status = 'READY', indexed_at = now()
			WHERE id = $1`, v1); err != nil {
			t.Fatal(err)
		}
		insertPolicyChunkFixture(t, ctx, tx, seed, v1)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		provenance := &ai.AnalysisProvenance{PolicyRefs: []ai.PolicyRef{
			policyRef(v1, "Historical", "Frozen v1 excerpt", 90000),
		}}
		if _, err := service.FinalizeCompleted(ctx, attempt.ID, attempt.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, provenance); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE policy_versions SET status = 'SUPERSEDED' WHERE id = $1`, v1); err != nil {
			t.Fatal(err)
		}
		v2 := insertPolicyVersionFixture(t, ctx, tx, seed, 2, policyID, "DRAFT")
		if _, err := tx.Exec(ctx, `
			UPDATE policy_versions
			SET status = 'ACTIVE', index_status = 'READY', indexed_at = now()
			WHERE id = $1`, v2); err != nil {
			t.Fatal(err)
		}
		if v1 == v2 {
			t.Fatal("fixture generated identical policy version IDs")
		}
		active, err := db.New(tx).GetActivePolicyVersion(ctx, policyID)
		if err != nil {
			t.Fatal(err)
		}
		if active.ID != v2 {
			t.Fatalf("active policy version = %v, want v2 %v", active.ID, v2)
		}
		refs, err := service.PolicyRefs(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 1 || refs[0].PolicyVersionID != v1 {
			t.Fatalf("refs = %+v, want old version %v", refs, v1)
		}
	})

	t.Run("verifier fail retains valid candidate provenance", func(t *testing.T) {
		const seed = 23
		caseID := insertCaseFixture(t, ctx, tx, seed)
		policyID := insertPolicyFixture(t, ctx, tx, seed, caseID)
		versionID := insertPolicyVersionFixture(t, ctx, tx, seed, 1, policyID, "DRAFT")
		evidenceID := insertEvidenceFixture(t, ctx, tx, seed, 1, caseID)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		candidate := validCandidate()
		vr := &ai.VerificationResult{Status: ai.VerificationStatusFail}
		provenance := &ai.AnalysisProvenance{
			PolicyRefs:   []ai.PolicyRef{policyRef(versionID, "Audit", "Verifier candidate context", 50000)},
			EvidenceRefs: []ai.EvidenceRef{{EvidenceID: evidenceID, UsageType: ai.EvidenceUsageReviewFeedback}},
		}
		failed, err := service.FinalizeFailed(ctx, attempt.ID, attempt.WorkerAttemptID, &candidate, vr, provenance)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Status != "FAILED" || !failed.Summary.Valid || failed.Summary.String != candidate.Summary {
			t.Fatalf("failed analysis = %+v", failed)
		}
		policyRefs, err := service.PolicyRefs(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		evidenceRefs, err := service.EvidenceRefs(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(policyRefs) != 1 || policyRefs[0].PolicyVersionID != versionID || len(evidenceRefs) != 1 || evidenceRefs[0].EvidenceID != evidenceID {
			t.Fatalf("stored verifier-fail refs = policy:%+v evidence:%+v", policyRefs, evidenceRefs)
		}
	})

	t.Run("early failure rejects fabricated refs then permits empty provenance", func(t *testing.T) {
		const seed = 24
		caseID := insertCaseFixture(t, ctx, tx, seed)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		fabricated := &ai.AnalysisProvenance{EvidenceRefs: []ai.EvidenceRef{{
			EvidenceID: fixtureUUID(10, seed), UsageType: ai.EvidenceUsageContext,
		}}}
		_, err = service.FinalizeFailed(ctx, attempt.ID, attempt.WorkerAttemptID, nil, nil, fabricated)
		if !errors.Is(err, ai.ErrInvalidProvenance) {
			t.Fatalf("error = %v, want ErrInvalidProvenance", err)
		}
		stored, err := db.New(tx).GetAnalysis(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != "GENERATING" {
			t.Fatalf("status after rejected refs = %q, want GENERATING", stored.Status)
		}
		assertNoStoredRefs(t, ctx, service, attempt.ID)

		failed, err := service.FinalizeFailed(ctx, attempt.ID, attempt.WorkerAttemptID, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Status != "FAILED" {
			t.Fatalf("status = %q, want FAILED", failed.Status)
		}
		assertResultFieldsNull(t, failed)
		assertNoStoredRefs(t, ctx, service, attempt.ID)
	})
}

func TestProvenanceReadAPINilReceiver(t *testing.T) {
	var service *Service
	if _, err := service.PolicyRefs(context.Background(), pgtype.UUID{}); err == nil {
		t.Fatal("PolicyRefs() error = nil")
	}
	if _, err := service.EvidenceRefs(context.Background(), pgtype.UUID{}); err == nil {
		t.Fatal("EvidenceRefs() error = nil")
	}
}

func policyRef(versionID pgtype.UUID, section, excerpt string, score int64) ai.PolicyRef {
	return ai.PolicyRef{
		PolicyVersionID: versionID,
		Section:         pgtype.Text{String: section, Valid: true},
		Excerpt:         pgtype.Text{String: excerpt, Valid: true},
		RelevanceScore:  pgtype.Numeric{Int: big.NewInt(score), Exp: -5, Valid: true},
	}
}

func assertStoredPolicyRef(t *testing.T, got db.AnalysisPolicyRef, section, excerpt string, score float64) {
	t.Helper()
	if !got.Section.Valid || got.Section.String != section || !got.Excerpt.Valid || got.Excerpt.String != excerpt {
		t.Errorf("policy ref = %+v", got)
	}
	value, err := got.RelevanceScore.Float64Value()
	if err != nil || !value.Valid || value.Float64 != score {
		t.Errorf("relevance = %+v (%v), want %v", got.RelevanceScore, err, score)
	}
}

func assertNoStoredRefs(t *testing.T, ctx context.Context, service *Service, analysisID pgtype.UUID) {
	t.Helper()
	policyRefs, err := service.PolicyRefs(ctx, analysisID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRefs, err := service.EvidenceRefs(ctx, analysisID)
	if err != nil {
		t.Fatal(err)
	}
	if policyRefs == nil || evidenceRefs == nil {
		t.Fatalf("empty ref lists must be non-nil: policy=%#v evidence=%#v", policyRefs, evidenceRefs)
	}
	if len(policyRefs) != 0 || len(evidenceRefs) != 0 {
		t.Fatalf("unexpected refs: policy=%+v evidence=%+v", policyRefs, evidenceRefs)
	}
}

func insertPolicyFixture(t *testing.T, ctx context.Context, tx db.DBTX, seed byte, caseID pgtype.UUID) pgtype.UUID {
	t.Helper()
	policyID := fixtureUUID(20, seed)
	tag := fmt.Sprintf("be032-policy-%d-%d", seed, time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `
		INSERT INTO policies (id, code, title, domain, case_type_id, description)
		SELECT $1, $2, $3, 'OPERATIONS', case_type_id, $4 FROM cases WHERE id = $5`,
		policyID, tag, "BE-032 policy", "Analysis provenance integration test", caseID); err != nil {
		t.Fatal(err)
	}
	return policyID
}

func insertPolicyVersionFixture(t *testing.T, ctx context.Context, tx db.DBTX, seed, version byte, policyID pgtype.UUID, status string) pgtype.UUID {
	t.Helper()
	versionID := fixtureUUID(20+version, seed)
	createdBy := fixtureUUID(2, seed)
	indexStatus := "NOT_STARTED"
	var indexedAt any
	if status == "ACTIVE" {
		indexStatus = "READY"
		indexedAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO policy_versions (
			id, policy_id, version, status, index_status, indexed_at, content, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		versionID, policyID, fmt.Sprintf("v%d", version), status, indexStatus, indexedAt,
		fmt.Sprintf("policy version %d", version), createdBy); err != nil {
		t.Fatal(err)
	}
	return versionID
}

func insertPolicyChunkFixture(t *testing.T, ctx context.Context, tx db.DBTX, seed byte, versionID pgtype.UUID) {
	t.Helper()
	vector := "[" + strings.TrimSuffix(strings.Repeat("0,", 768), ",") + "]"
	if _, err := tx.Exec(ctx, `
		INSERT INTO policy_chunks (id, policy_version_id, section, chunk_index, content, embedding)
		VALUES ($1, $2, 'Historical', 0, 'Frozen v1 excerpt', $3::vector)`,
		fixtureUUID(30, seed), versionID, vector); err != nil {
		t.Fatal(err)
	}
}

func insertEvidenceFixture(t *testing.T, ctx context.Context, tx db.DBTX, seed, ordinal byte, caseID pgtype.UUID) pgtype.UUID {
	t.Helper()
	evidenceID := fixtureUUID(40+ordinal, seed)
	if _, err := tx.Exec(ctx, `
		INSERT INTO case_evidences (id, case_id, source_type, evidence_type, title, content)
		VALUES ($1, $2, 'MAKER', 'DOCUMENT', $3, $4)`,
		evidenceID, caseID, fmt.Sprintf("Evidence %d", ordinal), "Evidence content"); err != nil {
		t.Fatal(err)
	}
	return evidenceID
}
