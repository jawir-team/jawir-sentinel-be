// Package aiworker coordinates durable AI-analysis claims and workflow
// finalization. PostgreSQL row locks and worker-attempt IDs are the only
// concurrency authority; the package keeps no in-memory claim state.
package aiworker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/analysisrepo"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	DefaultLeaseSeconds = int64(120)

	outboxEventAnalysisRequested = "AI_ANALYSIS_REQUESTED"
	auditAnalysisCompleted       = "AI_ANALYSIS_COMPLETED"
	auditVerifierFail            = "VERIFIER_FAIL"
	auditTechnicalExhausted      = "TECHNICAL_RETRY_EXHAUSTED"
)

var (
	// ErrStaleClaim is returned when a superseded worker attempts a mutation.
	ErrStaleClaim = analysisrepo.ErrStaleClaim

	ErrInvalidMessage = errors.New("AI worker message does not match persisted state")
	ErrNotConfigured  = errors.New("AI worker database is not configured")
)

// Database is implemented by *pgxpool.Pool and pgx.Tx. Accepting pgx.Tx lets
// integration tests keep fixtures inside a rollback-only outer transaction.
type Database interface {
	db.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// Outcome tells the message consumer whether it owns work and whether the
// delivery can be acknowledged. Every returned non-error outcome is durable.
type Outcome string

const (
	Claimed          Outcome = "CLAIMED"
	Duplicate        Outcome = "DUPLICATE"
	Finalized        Outcome = "FINALIZED"
	AlreadyFinalized Outcome = "ALREADY_FINALIZED"
)

type ClaimResult struct {
	Outcome         Outcome
	WorkerAttemptID pgtype.UUID
}

type SuccessResult struct {
	Candidate    ai.CandidateAnalysis
	Verification ai.VerificationResult
	Provenance   *ai.AnalysisProvenance
}

type FailedCandidate struct {
	Candidate    ai.CandidateAnalysis
	Verification ai.VerificationResult
	Provenance   *ai.AnalysisProvenance
}

type Worker struct {
	database     Database
	leaseSeconds float64
}

// New reads AI_WORKER_LEASE_SECONDS. Missing, invalid, non-positive, or
// overflowing values use the 120-second default.
func New(database Database) *Worker {
	return &Worker{database: database, leaseSeconds: float64(leaseSecondsFromEnv())}
}

// Claim owns only the short database phase. It commits before returning
// Claimed, so callers can safely begin external AI work after this method.
func (w *Worker) Claim(
	ctx context.Context,
	caseID, analysisID, outboxEventID pgtype.UUID,
) (ClaimResult, error) {
	if w == nil || w.database == nil {
		return ClaimResult{}, ErrNotConfigured
	}
	if !validUUID(caseID) || !validUUID(analysisID) || !validUUID(outboxEventID) {
		return ClaimResult{}, ErrInvalidMessage
	}

	tx, err := w.database.Begin(ctx)
	if err != nil {
		return ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)

	analysis, err := q.GetAnalysisForUpdate(ctx, analysisID)
	if err != nil {
		return ClaimResult{}, err
	}
	if !sameUUID(analysis.CaseID, caseID) {
		return ClaimResult{}, ErrInvalidMessage
	}
	storedCase, err := q.GetCaseForUpdate(ctx, caseID)
	if err != nil {
		return ClaimResult{}, err
	}
	outbox, err := q.GetOutboxEvent(ctx, outboxEventID)
	if err != nil {
		return ClaimResult{}, err
	}
	if !sameUUID(outbox.CaseID, caseID) ||
		!sameUUID(outbox.AnalysisID, analysisID) ||
		outbox.EventType != outboxEventAnalysisRequested {
		return ClaimResult{}, ErrInvalidMessage
	}

	if terminalAnalysis(analysis.Status) {
		result := ClaimResult{Outcome: AlreadyFinalized, WorkerAttemptID: analysis.WorkerAttemptID}
		if err := tx.Commit(ctx); err != nil {
			return ClaimResult{}, err
		}
		return result, nil
	}
	if analysis.Status != "GENERATING" || storedCase.Status != string(workflow.StateAIAnalysis) {
		return ClaimResult{}, ErrStaleClaim
	}

	active, err := q.IsAnalysisClaimActive(ctx, db.IsAnalysisClaimActiveParams{
		ID: analysisID, LeaseSeconds: w.leaseSeconds,
	})
	if err != nil {
		return ClaimResult{}, err
	}
	if active {
		result := ClaimResult{Outcome: Duplicate, WorkerAttemptID: analysis.WorkerAttemptID}
		if err := tx.Commit(ctx); err != nil {
			return ClaimResult{}, err
		}
		return result, nil
	}

	workerAttemptID, err := newUUID()
	if err != nil {
		return ClaimResult{}, fmt.Errorf("generate worker attempt ID: %w", err)
	}
	claimed, err := q.ClaimAnalysis(ctx, db.ClaimAnalysisParams{
		ID: analysisID, WorkerAttemptID: workerAttemptID, CaseID: caseID,
	})
	if err != nil {
		return ClaimResult{}, err
	}
	result := ClaimResult{Outcome: Claimed, WorkerAttemptID: claimed.WorkerAttemptID}
	if err := tx.Commit(ctx); err != nil {
		return ClaimResult{}, err
	}
	return result, nil
}

// FinalizeSuccess atomically stores the verified result and provenance,
// promotes it to current_analysis_id, audits completion, and enters CHECKING.
func (w *Worker) FinalizeSuccess(
	ctx context.Context,
	caseID, analysisID, workerAttemptID pgtype.UUID,
	result SuccessResult,
) (Outcome, error) {
	return w.finalize(ctx, caseID, analysisID, workerAttemptID, finalizeInput{
		event:      workflow.EventAnalysisSuccess,
		auditEvent: auditAnalysisCompleted,
		success:    &result,
	})
}

// VerifierFail retains the complete schema-valid candidate and verifier
// issues, leaves current_analysis_id unchanged, and escalates the case.
func (w *Worker) VerifierFail(
	ctx context.Context,
	caseID, analysisID, workerAttemptID pgtype.UUID,
	failed FailedCandidate,
) (Outcome, error) {
	return w.finalize(ctx, caseID, analysisID, workerAttemptID, finalizeInput{
		event:      workflow.EventAnalysisFailed,
		auditEvent: auditVerifierFail,
		failure:    &failed,
	})
}

// TechnicalRetry increments the retry counter and rotates the claim. The
// returned attempt ID must be used for every subsequent mutation.
func (w *Worker) TechnicalRetry(
	ctx context.Context,
	analysisID, workerAttemptID pgtype.UUID,
) (db.AiAnalysis, error) {
	if w == nil || w.database == nil {
		return db.AiAnalysis{}, ErrNotConfigured
	}
	return analysisrepo.New(w.database).RetryAttempt(ctx, analysisID, workerAttemptID)
}

// TechnicalExhaustion stores a result-less FAILED attempt, leaves
// current_analysis_id unchanged, and escalates the case.
func (w *Worker) TechnicalExhaustion(
	ctx context.Context,
	caseID, analysisID, workerAttemptID pgtype.UUID,
) (Outcome, error) {
	return w.finalize(ctx, caseID, analysisID, workerAttemptID, finalizeInput{
		event:      workflow.EventAnalysisFailed,
		auditEvent: auditTechnicalExhausted,
	})
}

type finalizeInput struct {
	event      workflow.Event
	auditEvent string
	success    *SuccessResult
	failure    *FailedCandidate
}

func (w *Worker) finalize(
	ctx context.Context,
	caseID, analysisID, workerAttemptID pgtype.UUID,
	input finalizeInput,
) (Outcome, error) {
	if w == nil || w.database == nil {
		return "", ErrNotConfigured
	}
	if !validUUID(caseID) || !validUUID(analysisID) || !validUUID(workerAttemptID) {
		return "", ErrInvalidMessage
	}

	tx, err := w.database.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)

	analysis, err := q.GetAnalysisForUpdate(ctx, analysisID)
	if err != nil {
		return "", err
	}
	if !sameUUID(analysis.CaseID, caseID) {
		return "", ErrInvalidMessage
	}
	storedCase, err := q.GetCaseForUpdate(ctx, caseID)
	if err != nil {
		return "", err
	}
	if terminalAnalysis(analysis.Status) {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return AlreadyFinalized, nil
	}
	if analysis.Status != "GENERATING" ||
		!sameUUID(analysis.WorkerAttemptID, workerAttemptID) ||
		storedCase.Status != string(workflow.StateAIAnalysis) {
		return "", ErrStaleClaim
	}

	nextState, err := workflow.Transition(workflow.State(storedCase.Status), input.event)
	if err != nil {
		return "", err
	}
	repository := analysisrepo.New(tx)
	switch {
	case input.success != nil:
		_, err = repository.FinalizeCompleted(
			ctx, analysisID, workerAttemptID,
			input.success.Candidate, input.success.Verification, input.success.Provenance,
		)
	case input.failure != nil:
		_, err = repository.FinalizeFailed(
			ctx, analysisID, workerAttemptID,
			&input.failure.Candidate, &input.failure.Verification, input.failure.Provenance,
		)
	default:
		_, err = repository.FinalizeFailed(ctx, analysisID, workerAttemptID, nil, nil, nil)
	}
	if err != nil {
		if errors.Is(err, analysisrepo.ErrStaleClaim) {
			return "", ErrStaleClaim
		}
		return "", err
	}

	auditID, err := newUUID()
	if err != nil {
		return "", fmt.Errorf("generate audit event ID: %w", err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     caseID,
		EventType:  input.auditEvent,
		AnalysisID: analysisID,
		Metadata:   []byte(`{}`),
	}); err != nil {
		return "", err
	}
	if _, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{
		ID: caseID, Status: string(nextState),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return Finalized, nil
}

func leaseSecondsFromEnv() int64 {
	seconds, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("AI_WORKER_LEASE_SECONDS")), 10, 64)
	maxSeconds := int64(time.Duration(1<<63-1) / time.Second)
	if err != nil || seconds <= 0 || seconds > maxSeconds {
		return DefaultLeaseSeconds
	}
	return seconds
}

func terminalAnalysis(status string) bool {
	return status == "COMPLETED" || status == "FAILED"
}

func sameUUID(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && a.Bytes == b.Bytes
}

func validUUID(id pgtype.UUID) bool {
	return id.Valid && id.Bytes != [16]byte{}
}

func newUUID() (pgtype.UUID, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return pgtype.UUID{}, err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
