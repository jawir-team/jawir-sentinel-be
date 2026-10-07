package aiworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

func TestLeaseSecondsFromEnv(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "default", raw: "", want: DefaultLeaseSeconds},
		{name: "configured", raw: "45", want: 45},
		{name: "not a number", raw: "later", want: DefaultLeaseSeconds},
		{name: "non-positive", raw: "0", want: DefaultLeaseSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AI_WORKER_LEASE_SECONDS", tt.raw)
			if got := leaseSecondsFromEnv(); got != tt.want {
				t.Fatalf("leaseSecondsFromEnv() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClaimOutcomes(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	t.Setenv("AI_WORKER_LEASE_SECONDS", "120")
	worker := New(tx)

	tests := []struct {
		name      string
		prepare   func(testing.TB, context.Context, pgx.Tx, workerFixture) pgtype.UUID
		want      Outcome
		wantFresh bool
	}{
		{
			name:      "fresh analysis with no claim",
			want:      Claimed,
			wantFresh: true,
		},
		{
			name: "active claim is a duplicate",
			prepare: func(t testing.TB, ctx context.Context, tx pgx.Tx, fixture workerFixture) pgtype.UUID {
				attemptID := mustUUID(t)
				if _, err := tx.Exec(ctx, `UPDATE ai_analyses SET worker_attempt_id = $2, worker_started_at = now() WHERE id = $1`, fixture.analysisID, attemptID); err != nil {
					t.Fatal(err)
				}
				return attemptID
			},
			want: Duplicate,
		},
		{
			name: "stale lease is rotated",
			prepare: func(t testing.TB, ctx context.Context, tx pgx.Tx, fixture workerFixture) pgtype.UUID {
				attemptID := mustUUID(t)
				if _, err := tx.Exec(ctx, `UPDATE ai_analyses SET worker_attempt_id = $2, worker_started_at = now() - interval '10 minutes' WHERE id = $1`, fixture.analysisID, attemptID); err != nil {
					t.Fatal(err)
				}
				return attemptID
			},
			want:      Claimed,
			wantFresh: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := insertWorkerFixture(t, ctx, tx)
			var previous pgtype.UUID
			if tt.prepare != nil {
				previous = tt.prepare(t, ctx, tx, fixture)
			}
			result, err := worker.Claim(ctx, fixture.caseID, fixture.analysisID, fixture.outboxID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != tt.want {
				t.Fatalf("outcome = %q, want %q", result.Outcome, tt.want)
			}
			if !validUUID(result.WorkerAttemptID) {
				t.Fatal("worker attempt ID is empty")
			}
			if tt.wantFresh && sameUUID(result.WorkerAttemptID, previous) {
				t.Fatalf("claim was not rotated: %v", result.WorkerAttemptID)
			}
			if !tt.wantFresh && !sameUUID(result.WorkerAttemptID, previous) {
				t.Fatalf("duplicate returned attempt %v, want %v", result.WorkerAttemptID, previous)
			}
			stored, err := db.New(tx).GetAnalysis(ctx, fixture.analysisID)
			if err != nil {
				t.Fatal(err)
			}
			if !sameUUID(stored.WorkerAttemptID, result.WorkerAttemptID) || !stored.WorkerStartedAt.Valid {
				t.Fatalf("stored claim = %+v, result = %+v", stored, result)
			}
		})
	}

	t.Run("terminal analysis is an acknowledged no-op", func(t *testing.T) {
		fixture := insertWorkerFixture(t, ctx, tx)
		if _, err := tx.Exec(ctx, `
			UPDATE ai_analyses SET status = 'FAILED' WHERE id = $1`, fixture.analysisID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE cases SET status = 'ESCALATION_REQUIRED' WHERE id = $1`, fixture.caseID); err != nil {
			t.Fatal(err)
		}
		result, err := worker.Claim(ctx, fixture.caseID, fixture.analysisID, fixture.outboxID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != AlreadyFinalized {
			t.Fatalf("outcome = %q, want %q", result.Outcome, AlreadyFinalized)
		}
	})
}

func TestClaimLeaseStartsAtClaimMutation(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	fixture := insertWorkerFixture(t, ctx, tx)
	var beforeClaim time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&beforeClaim); err != nil {
		t.Fatal(err)
	}

	attemptID := mustClaim(t, ctx, New(tx), fixture)
	stored, err := db.New(tx).GetAnalysis(ctx, fixture.analysisID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameUUID(stored.WorkerAttemptID, attemptID) || stored.WorkerStartedAt.Time.Before(beforeClaim) {
		t.Fatalf("stored claim timestamp = %v, want at or after mutation baseline %v", stored.WorkerStartedAt, beforeClaim)
	}
}

func TestFinalizationFencingAndIdempotency(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	t.Setenv("AI_WORKER_LEASE_SECONDS", "120")
	worker := New(tx)

	t.Run("superseded attempt cannot finalize", func(t *testing.T) {
		fixture := insertWorkerFixture(t, ctx, tx)
		first := mustClaim(t, ctx, worker, fixture)
		if _, err := tx.Exec(ctx, `UPDATE ai_analyses SET worker_started_at = now() - interval '10 minutes' WHERE id = $1`, fixture.analysisID); err != nil {
			t.Fatal(err)
		}
		second := mustClaim(t, ctx, worker, fixture)
		if sameUUID(first, second) {
			t.Fatal("stale delivery did not rotate the claim")
		}

		_, err := worker.FinalizeSuccess(ctx, fixture.caseID, fixture.analysisID, first, validSuccessResult())
		if !errors.Is(err, ErrStaleClaim) {
			t.Fatalf("FinalizeSuccess() error = %v, want ErrStaleClaim", err)
		}
		stored, err := db.New(tx).GetAnalysis(ctx, fixture.analysisID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != "GENERATING" || !sameUUID(stored.WorkerAttemptID, second) {
			t.Fatalf("analysis mutated by stale worker: %+v", stored)
		}
	})

	t.Run("double finalize returns AlreadyFinalized", func(t *testing.T) {
		fixture := insertWorkerFixture(t, ctx, tx)
		attemptID := mustClaim(t, ctx, worker, fixture)
		outcome, err := worker.FinalizeSuccess(ctx, fixture.caseID, fixture.analysisID, attemptID, validSuccessResult())
		if err != nil {
			t.Fatal(err)
		}
		if outcome != Finalized {
			t.Fatalf("first outcome = %q, want %q", outcome, Finalized)
		}
		outcome, err = worker.FinalizeSuccess(ctx, fixture.caseID, fixture.analysisID, attemptID, validSuccessResult())
		if err != nil {
			t.Fatal(err)
		}
		if outcome != AlreadyFinalized {
			t.Fatalf("second outcome = %q, want %q", outcome, AlreadyFinalized)
		}

		storedCase, err := db.New(tx).GetCase(ctx, fixture.caseID)
		if err != nil {
			t.Fatal(err)
		}
		if storedCase.Status != "CHECKING" || !sameUUID(storedCase.CurrentAnalysisID, fixture.analysisID) {
			t.Fatalf("finalized case = %+v", storedCase)
		}
		assertAuditCount(t, ctx, tx, fixture.caseID, "AI_ANALYSIS_COMPLETED", 1)
	})
}

func TestTechnicalRetryFencing(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	worker := New(tx)
	fixture := insertWorkerFixture(t, ctx, tx)
	first := mustClaim(t, ctx, worker, fixture)

	retried, err := worker.TechnicalRetry(ctx, fixture.analysisID, first)
	if err != nil {
		t.Fatal(err)
	}
	if retried.TechnicalRetryCount != 1 || sameUUID(retried.WorkerAttemptID, first) {
		t.Fatalf("retried analysis = %+v", retried)
	}
	if _, err := worker.TechnicalRetry(ctx, fixture.analysisID, first); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("superseded TechnicalRetry() error = %v, want ErrStaleClaim", err)
	}
	stored, err := db.New(tx).GetAnalysis(ctx, fixture.analysisID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TechnicalRetryCount != 1 || !sameUUID(stored.WorkerAttemptID, retried.WorkerAttemptID) {
		t.Fatalf("superseded retry mutated analysis: %+v", stored)
	}

	if _, err := tx.Exec(ctx, `UPDATE ai_analyses SET worker_started_at = now() - interval '10 minutes' WHERE id = $1`, fixture.analysisID); err != nil {
		t.Fatal(err)
	}
	reclaimed := mustClaim(t, ctx, worker, fixture)
	stored, err = db.New(tx).GetAnalysis(ctx, fixture.analysisID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TechnicalRetryCount != 1 || !sameUUID(stored.WorkerAttemptID, reclaimed) {
		t.Fatalf("stale-lease recovery reset persisted retry state: %+v", stored)
	}
}

func TestTechnicalRetryLeaseStartsAtRetryMutation(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	fixture := insertWorkerFixture(t, ctx, tx)
	attemptID := mustClaim(t, ctx, New(tx), fixture)
	var beforeRetry time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&beforeRetry); err != nil {
		t.Fatal(err)
	}

	retried, err := New(tx).TechnicalRetry(ctx, fixture.analysisID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.WorkerStartedAt.Time.Before(beforeRetry) {
		t.Fatalf("retry claim timestamp = %v, want at or after mutation baseline %v", retried.WorkerStartedAt, beforeRetry)
	}
}

func TestFailureFinalization(t *testing.T) {
	pool, ctx := openWorkerTestDatabase(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	worker := New(tx)

	tests := []struct {
		name       string
		auditEvent string
		finalize   func(context.Context, *Worker, workerFixture, pgtype.UUID) (Outcome, error)
		wantOutput bool
	}{
		{
			name:       "technical exhaustion",
			auditEvent: "TECHNICAL_RETRY_EXHAUSTED",
			finalize: func(ctx context.Context, worker *Worker, fixture workerFixture, attemptID pgtype.UUID) (Outcome, error) {
				return worker.TechnicalExhaustion(ctx, fixture.caseID, fixture.analysisID, attemptID)
			},
		},
		{
			name:       "verifier failure retains candidate",
			auditEvent: "VERIFIER_FAIL",
			finalize: func(ctx context.Context, worker *Worker, fixture workerFixture, attemptID pgtype.UUID) (Outcome, error) {
				candidate := validCandidate()
				return worker.VerifierFail(ctx, fixture.caseID, fixture.analysisID, attemptID, FailedCandidate{
					Candidate: candidate,
					Verification: ai.VerificationResult{
						Status: ai.VerificationStatusFail,
						Issues: []ai.VerifierIssue{{
							Type: ai.VerifierIssueUnsupportedClaim, Severity: ai.IssueSeverityCritical,
							Description: "unsupported", RelatedRefs: []string{},
						}},
					},
				})
			},
			wantOutput: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := insertWorkerFixture(t, ctx, tx)
			attemptID := mustClaim(t, ctx, worker, fixture)
			outcome, err := tt.finalize(ctx, worker, fixture, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != Finalized {
				t.Fatalf("outcome = %q, want %q", outcome, Finalized)
			}
			stored, err := db.New(tx).GetAnalysis(ctx, fixture.analysisID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != "FAILED" || stored.Summary.Valid != tt.wantOutput {
				t.Fatalf("failed analysis = %+v", stored)
			}
			storedCase, err := db.New(tx).GetCase(ctx, fixture.caseID)
			if err != nil {
				t.Fatal(err)
			}
			if storedCase.Status != "ESCALATION_REQUIRED" || storedCase.CurrentAnalysisID.Valid {
				t.Fatalf("escalated case = %+v", storedCase)
			}
			assertAuditCount(t, ctx, tx, fixture.caseID, tt.auditEvent, 1)
		})
	}
}

type workerFixture struct {
	caseID     pgtype.UUID
	analysisID pgtype.UUID
	outboxID   pgtype.UUID
}

func insertWorkerFixture(t testing.TB, ctx context.Context, tx pgx.Tx) workerFixture {
	t.Helper()
	unitID := mustUUID(t)
	userID := mustUUID(t)
	caseID := mustUUID(t)
	analysisID := mustUUID(t)
	outboxID := mustUUID(t)
	tag := fmt.Sprintf("be033-%x", caseID.Bytes)
	if _, err := tx.Exec(ctx, `INSERT INTO units (id, code, name) VALUES ($1, $2, 'BE-033 test unit')`, unitID, tag); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, unit_id, firebase_uid, name, email)
		VALUES ($1, $2, $3, 'BE-033 test user', $4)`,
		userID, unitID, tag, tag+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cases (
			id, case_number, case_type_id, title, description, urgency,
			status, created_by, owner_id
		)
		SELECT $1, $2, id, 'BE-033 test case', 'AI worker integration test',
		       'LOW', 'AI_ANALYSIS', $3, $3
		FROM case_types ORDER BY code LIMIT 1`, caseID, tag, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ai_analyses (
			id, case_id, version, status, model_name, prompt_version
		) VALUES ($1, $2, 1, 'GENERATING', 'test-model', 'prompt-v1')`,
		analysisID, caseID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (id, case_id, analysis_id, event_type, payload)
		VALUES ($1, $2, $3, 'AI_ANALYSIS_REQUESTED', '{}'::jsonb)`,
		outboxID, caseID, analysisID); err != nil {
		t.Fatal(err)
	}
	return workerFixture{caseID: caseID, analysisID: analysisID, outboxID: outboxID}
}

func mustClaim(t testing.TB, ctx context.Context, worker *Worker, fixture workerFixture) pgtype.UUID {
	t.Helper()
	result, err := worker.Claim(ctx, fixture.caseID, fixture.analysisID, fixture.outboxID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Claimed {
		t.Fatalf("Claim() outcome = %q, want %q", result.Outcome, Claimed)
	}
	return result.WorkerAttemptID
}

func validSuccessResult() SuccessResult {
	return SuccessResult{
		Candidate: validCandidate(),
		Verification: ai.VerificationResult{
			Status: ai.VerificationStatusPass,
			Issues: []ai.VerifierIssue{},
		},
	}
}

func validCandidate() ai.CandidateAnalysis {
	candidate := ai.NewCandidateAnalysis()
	candidate.Summary = "Verified analysis"
	candidate.PolicyStatus = ai.PolicyStatusFound
	candidate.EvidenceQuality = ai.QualityHigh
	candidate.Uncertainty = ai.QualityLow
	candidate.ComplianceAnalysis.Status = ai.ComplianceNoIssueIdentified
	candidate.ComplianceAnalysis.Reason = "No compliance issue identified"
	candidate.Recommendation.Type = ai.RecommendationTypePolicyBased
	candidate.Recommendation.Summary = "Proceed under policy"
	return candidate
}

func assertAuditCount(t testing.TB, ctx context.Context, tx pgx.Tx, caseID pgtype.UUID, eventType string, want int) {
	t.Helper()
	var got int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE case_id = $1 AND event_type = $2`, caseID, eventType).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("audit count for %s = %d, want %d", eventType, got, want)
	}
}

func mustUUID(t testing.TB) pgtype.UUID {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func openWorkerTestDatabase(t *testing.T) (*pgxpool.Pool, context.Context) {
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
