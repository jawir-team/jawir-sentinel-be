package analysisrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

func TestEncodeCandidatePreservesEmptyCollections(t *testing.T) {
	encoded, err := encodeCandidate(validCandidate())
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{
		"facts":               encoded.facts,
		"assumptions":         encoded.assumptions,
		"unknowns":            encoded.unknowns,
		"risk_analysis":       encoded.riskAnalysis,
		"alternatives":        encoded.alternatives,
		"missing_information": encoded.missingInformation,
	} {
		if string(got) != "[]" {
			t.Errorf("%s = %s, want []", name, got)
		}
	}
}

func TestEncodeCandidateRejectsIncompleteResultWithSentinel(t *testing.T) {
	_, err := encodeCandidate(ai.CandidateAnalysis{Summary: "present"})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("error = %v, want ErrInvalidResult", err)
	}
	if !strings.Contains(err.Error(), "facts") || !strings.Contains(err.Error(), "recommendation") {
		t.Fatalf("error = %q, want missing fields listed", err)
	}
}

func TestAnalysisLifecyclePersistence(t *testing.T) {
	pool, ctx := openTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	service := New(tx)

	t.Run("initial generating and terminal failure without result", func(t *testing.T) {
		caseID := insertCaseFixture(t, ctx, tx, 1)
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		assertInitialGenerating(t, attempt)

		failed, err := service.FinalizeFailed(ctx, attempt.ID, attempt.WorkerAttemptID, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Status != "FAILED" || failed.VerificationStatus.Valid {
			t.Errorf("failed attempt = %+v", failed)
		}
		assertResultFieldsNull(t, failed)
		if _, err := service.Current(ctx, caseID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("Current() error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("completed v1 remains current after failed v2", func(t *testing.T) {
		caseID := insertCaseFixture(t, ctx, tx, 2)
		v1, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		completed, err := service.FinalizeCompleted(ctx, v1.ID, v1.WorkerAttemptID, validCandidate(), ai.VerificationResult{
			Status: ai.VerificationStatusPass,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status != "COMPLETED" || completed.Version != 1 {
			t.Fatalf("completed = %+v", completed)
		}
		current, err := service.Current(ctx, caseID)
		if err != nil {
			t.Fatal(err)
		}
		if current.ID != v1.ID {
			t.Fatalf("current ID = %v, want v1 %v", current.ID, v1.ID)
		}

		v2, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v2")
		if err != nil {
			t.Fatal(err)
		}
		if v2.Version != 2 {
			t.Fatalf("v2 version = %d", v2.Version)
		}
		if _, err := service.FinalizeFailed(ctx, v2.ID, v2.WorkerAttemptID, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		current, err = service.Current(ctx, caseID)
		if err != nil {
			t.Fatal(err)
		}
		if current.ID != v1.ID {
			t.Fatalf("current ID after failed v2 = %v, want v1 %v", current.ID, v1.ID)
		}
		latest, err := service.Latest(ctx, caseID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.ID != v2.ID || latest.Status != "FAILED" {
			t.Fatalf("latest = %+v, want failed v2", latest)
		}
		history, err := service.History(ctx, caseID)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 || history[0].Version != 2 || history[1].Version != 1 {
			t.Fatalf("history versions = %v, want [2 1]", analysisVersions(history))
		}
	})

	t.Run("verifier failure retains structured data and empty arrays", func(t *testing.T) {
		caseID := insertCaseFixture(t, ctx, tx, 3)
		currentAttempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.FinalizeCompleted(ctx, currentAttempt.ID, currentAttempt.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, nil); err != nil {
			t.Fatal(err)
		}
		attempt, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v2")
		if err != nil {
			t.Fatal(err)
		}
		candidate := validCandidate()
		vr := &ai.VerificationResult{
			Status: ai.VerificationStatusFail,
			Issues: []ai.VerifierIssue{{
				Type:        ai.VerifierIssueUnsupportedClaim,
				Severity:    ai.IssueSeverityCritical,
				Description: "claim is not grounded",
				RelatedRefs: []string{},
			}},
		}
		failed, err := service.FinalizeFailed(ctx, attempt.ID, attempt.WorkerAttemptID, &candidate, vr, nil)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Status != "FAILED" || !failed.VerificationStatus.Valid || failed.VerificationStatus.String != "FAIL" {
			t.Fatalf("failed verifier attempt = %+v", failed)
		}
		if !failed.Summary.Valid || failed.Summary.String != candidate.Summary {
			t.Errorf("summary = %+v", failed.Summary)
		}
		for name, got := range map[string][]byte{
			"facts":               failed.Facts,
			"assumptions":         failed.Assumptions,
			"unknowns":            failed.Unknowns,
			"risk_analysis":       failed.RiskAnalysis,
			"alternatives":        failed.Alternatives,
			"missing_information": failed.MissingInformation,
		} {
			if string(got) != "[]" {
				t.Errorf("stored %s = %s, want []", name, got)
			}
		}
		wantNotes, err := vr.VerificationNotesJSON()
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(failed.VerificationNotes, wantNotes) {
			t.Errorf("verification notes = %s, want %s", failed.VerificationNotes, wantNotes)
		}
		current, err := service.Current(ctx, caseID)
		if err != nil {
			t.Fatal(err)
		}
		if current.ID != currentAttempt.ID {
			t.Fatalf("current ID after verifier failure = %v, want v1 %v", current.ID, currentAttempt.ID)
		}
	})

	t.Run("retry reclaim fencing and immutability", func(t *testing.T) {
		caseID := insertCaseFixture(t, ctx, tx, 4)
		initial, err := service.BeginAttempt(ctx, caseID, "test-model", "prompt-v1")
		if err != nil {
			t.Fatal(err)
		}
		retried, err := service.RetryAttempt(ctx, initial.ID, initial.WorkerAttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if retried.Version != initial.Version || retried.TechnicalRetryCount != 1 {
			t.Fatalf("retried = %+v", retried)
		}
		if retried.WorkerAttemptID == initial.WorkerAttemptID {
			t.Fatal("technical retry did not rotate worker_attempt_id")
		}

		reclaimed, err := service.ReclaimAttempt(ctx, initial.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reclaimed.TechnicalRetryCount != 1 || reclaimed.WorkerAttemptID == retried.WorkerAttemptID {
			t.Fatalf("reclaimed = %+v", reclaimed)
		}
		_, err = service.FinalizeCompleted(ctx, initial.ID, retried.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, nil)
		if !errors.Is(err, ErrStaleClaim) {
			t.Fatalf("old claim FinalizeCompleted() error = %v, want ErrStaleClaim", err)
		}
		_, err = service.RetryAttempt(ctx, initial.ID, retried.WorkerAttemptID)
		if !errors.Is(err, ErrStaleClaim) {
			t.Fatalf("old claim RetryAttempt() error = %v, want ErrStaleClaim", err)
		}

		if _, err := service.FinalizeCompleted(ctx, initial.ID, reclaimed.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, nil); err != nil {
			t.Fatal(err)
		}
		_, err = service.FinalizeCompleted(ctx, initial.ID, reclaimed.WorkerAttemptID, validCandidate(), ai.VerificationResult{Status: ai.VerificationStatusPass}, nil)
		if !errors.Is(err, ErrImmutable) {
			t.Fatalf("second FinalizeCompleted() error = %v, want ErrImmutable", err)
		}
		_, err = service.ReclaimAttempt(ctx, initial.ID)
		if !errors.Is(err, ErrImmutable) {
			t.Fatalf("terminal ReclaimAttempt() error = %v, want ErrImmutable", err)
		}
	})

	t.Run("versions are monotone", func(t *testing.T) {
		caseID := insertCaseFixture(t, ctx, tx, 5)
		versions := make([]int32, 0, 3)
		for i := 0; i < 3; i++ {
			attempt, err := service.BeginAttempt(ctx, caseID, "test-model", fmt.Sprintf("prompt-v%d", i+1))
			if err != nil {
				t.Fatal(err)
			}
			versions = append(versions, attempt.Version)
		}
		if !slices.Equal(versions, []int32{1, 2, 3}) {
			t.Fatalf("versions = %v, want [1 2 3]", versions)
		}
	})
}

func TestConcurrentVersionAllocation(t *testing.T) {
	pool, ctx := openTestDatabase(t)
	caseID, cleanup := insertCommittedCaseFixture(t, ctx, pool)
	t.Cleanup(cleanup)
	service := New(pool)

	const workers = 8
	versions := make(chan int32, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			attempt, err := service.BeginAttempt(ctx, caseID, "test-model", fmt.Sprintf("concurrent-%d", worker))
			if err != nil {
				errs <- err
				return
			}
			versions <- attempt.Version
		}(i)
	}
	wg.Wait()
	close(errs)
	close(versions)
	for err := range errs {
		t.Errorf("BeginAttempt() error = %v", err)
	}
	if t.Failed() {
		return
	}
	got := make([]int, 0, workers)
	for version := range versions {
		got = append(got, int(version))
	}
	sort.Ints(got)
	want := []int{1, 2, 3, 4, 5, 6, 7, 8}
	if !slices.Equal(got, want) {
		t.Fatalf("concurrent versions = %v, want %v", got, want)
	}
}

func validCandidate() ai.CandidateAnalysis {
	candidate := ai.NewCandidateAnalysis()
	candidate.Summary = "Validated analysis summary"
	candidate.PolicyStatus = ai.PolicyStatusFound
	candidate.EvidenceQuality = ai.QualityHigh
	candidate.Uncertainty = ai.QualityLow
	candidate.ComplianceAnalysis.Status = ai.ComplianceNoIssueIdentified
	candidate.ComplianceAnalysis.Reason = "No compliance issue was identified"
	candidate.Recommendation.Type = ai.RecommendationTypePolicyBased
	candidate.Recommendation.Summary = "Proceed under the applicable policy"
	return candidate
}

func assertInitialGenerating(t *testing.T, attempt db.AiAnalysis) {
	t.Helper()
	if attempt.Status != "GENERATING" || attempt.Version != 1 || attempt.TechnicalRetryCount != 0 {
		t.Errorf("initial attempt state = %+v", attempt)
	}
	if !attempt.WorkerAttemptID.Valid || !attempt.WorkerStartedAt.Valid {
		t.Errorf("initial worker fields = id:%+v started:%+v", attempt.WorkerAttemptID, attempt.WorkerStartedAt)
	}
	assertResultFieldsNull(t, attempt)
}

func assertResultFieldsNull(t *testing.T, analysis db.AiAnalysis) {
	t.Helper()
	if analysis.Summary.Valid ||
		analysis.Facts != nil || analysis.Assumptions != nil || analysis.Unknowns != nil ||
		analysis.RiskAnalysis != nil || analysis.ComplianceAnalysis != nil ||
		analysis.Recommendation != nil || analysis.Alternatives != nil ||
		analysis.MissingInformation != nil || analysis.PolicyStatus.Valid ||
		analysis.EvidenceQuality.Valid || analysis.Uncertainty.Valid ||
		analysis.VerificationStatus.Valid || analysis.VerificationNotes != nil {
		t.Errorf("result fields were populated: %+v", analysis)
	}
}

func analysisVersions(history []db.AiAnalysis) []int32 {
	versions := make([]int32, len(history))
	for i := range history {
		versions[i] = history[i].Version
	}
	return versions
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func openTestDatabase(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		t.Skip("set TEST_DATABASE_URL or DATABASE_URL to a local PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	pool, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func insertCaseFixture(t *testing.T, ctx context.Context, tx pgx.Tx, seed byte) pgtype.UUID {
	t.Helper()
	unitID := fixtureUUID(1, seed)
	userID := fixtureUUID(2, seed)
	caseID := fixtureUUID(3, seed)
	tag := fmt.Sprintf("be031-%d-%d", seed, time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `INSERT INTO units (id, code, name) VALUES ($1, $2, $3)`, unitID, tag, "BE-031 test unit"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, unit_id, firebase_uid, name, email)
		VALUES ($1, $2, $3, $4, $5)`,
		userID, unitID, tag, "BE-031 test user", tag+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cases (
			id, case_number, case_type_id, title, description, urgency, created_by, owner_id
		)
		SELECT $1, $2, id, $3, $4, 'LOW', $5, $5
		FROM case_types ORDER BY code LIMIT 1`,
		caseID, tag, "BE-031 test case", "Analysis repository integration test", userID); err != nil {
		t.Fatal(err)
	}
	return caseID
}

func insertCommittedCaseFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (pgtype.UUID, func()) {
	t.Helper()
	unitID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	userID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	caseID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("be031-concurrent-%x", caseID.Bytes)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO units (id, code, name) VALUES ($1, $2, $3)`, unitID, tag, "BE-031 concurrent unit"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, unit_id, firebase_uid, name, email)
		VALUES ($1, $2, $3, $4, $5)`,
		userID, unitID, tag, "BE-031 concurrent user", tag+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cases (
			id, case_number, case_type_id, title, description, urgency, created_by, owner_id
		)
		SELECT $1, $2, id, $3, $4, 'LOW', $5, $5
		FROM case_types ORDER BY code LIMIT 1`,
		caseID, tag, "BE-031 concurrent case", "Analysis repository concurrency test", userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return caseID, func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleanupTx, err := pool.Begin(cleanupCtx)
		if err != nil {
			t.Errorf("begin fixture cleanup: %v", err)
			return
		}
		defer func() { _ = cleanupTx.Rollback(context.Background()) }()
		if _, err := cleanupTx.Exec(cleanupCtx, `DELETE FROM cases WHERE id = $1`, caseID); err != nil {
			t.Errorf("delete fixture case: %v", err)
			return
		}
		if _, err := cleanupTx.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("delete fixture user: %v", err)
			return
		}
		if _, err := cleanupTx.Exec(cleanupCtx, `DELETE FROM units WHERE id = $1`, unitID); err != nil {
			t.Errorf("delete fixture unit: %v", err)
			return
		}
		if err := cleanupTx.Commit(cleanupCtx); err != nil {
			t.Errorf("commit fixture cleanup: %v", err)
		}
	}
}

func fixtureUUID(kind, seed byte) pgtype.UUID {
	var id [16]byte
	id[0] = 0xbe
	id[1] = 0x31
	id[14] = kind
	id[15] = seed
	return pgtype.UUID{Bytes: id, Valid: true}
}
