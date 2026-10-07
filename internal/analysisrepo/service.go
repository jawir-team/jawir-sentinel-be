// Package analysisrepo persists versioned AI analysis attempts and enforces
// worker-claim fencing and terminal immutability.
package analysisrepo

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

var (
	ErrStaleClaim    = errors.New("analysis worker claim is stale")
	ErrImmutable     = errors.New("terminal analysis is immutable")
	ErrInvalidResult = errors.New("analysis result is invalid")
)

// Database is the database boundary needed by Service. Both *pgxpool.Pool and
// pgx.Tx implement it, which also lets integration tests wrap all work in a
// rollback-only outer transaction.
type Database interface {
	db.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// Service owns the transaction boundaries for version allocation, claim
// rotation, and terminal transitions.
type Service struct {
	database Database
}

func New(database Database) *Service {
	return &Service{database: database}
}

// BeginAttempt allocates a monotonically increasing per-case version while an
// advisory transaction lock serializes concurrent allocators for that case.
func (s *Service) BeginAttempt(
	ctx context.Context,
	caseID pgtype.UUID,
	modelName, promptVersion string,
) (db.AiAnalysis, error) {
	analysisID, err := newUUID()
	if err != nil {
		return db.AiAnalysis{}, fmt.Errorf("generate analysis ID: %w", err)
	}
	workerAttemptID, err := newUUID()
	if err != nil {
		return db.AiAnalysis{}, fmt.Errorf("generate worker attempt ID: %w", err)
	}

	return s.inTx(ctx, func(q *db.Queries) (db.AiAnalysis, error) {
		if err := q.LockAnalysisVersionSeq(ctx, caseID); err != nil {
			return db.AiAnalysis{}, err
		}
		maxVersion, err := q.MaxAnalysisVersion(ctx, caseID)
		if err != nil {
			return db.AiAnalysis{}, err
		}
		if maxVersion == math.MaxInt32 {
			return db.AiAnalysis{}, errors.New("analysis version limit reached")
		}
		return q.CreateGeneratingAnalysis(ctx, db.CreateGeneratingAnalysisParams{
			ID:              analysisID,
			CaseID:          caseID,
			Version:         maxVersion + 1,
			WorkerAttemptID: workerAttemptID,
			ModelName:       modelName,
			PromptVersion:   promptVersion,
		})
	})
}

// RetryAttempt records a technical retry on the same version and rotates the
// worker claim. Only the currently claimed worker can perform the retry.
func (s *Service) RetryAttempt(
	ctx context.Context,
	analysisID, currentWorkerAttemptID pgtype.UUID,
) (db.AiAnalysis, error) {
	newWorkerAttemptID, err := newUUID()
	if err != nil {
		return db.AiAnalysis{}, fmt.Errorf("generate worker attempt ID: %w", err)
	}
	return s.inTx(ctx, func(q *db.Queries) (db.AiAnalysis, error) {
		if _, err := requireGenerating(ctx, q, analysisID, &currentWorkerAttemptID); err != nil {
			return db.AiAnalysis{}, err
		}
		return q.BumpTechnicalRetry(ctx, db.BumpTechnicalRetryParams{
			ID:                     analysisID,
			CurrentWorkerAttemptID: currentWorkerAttemptID,
			NewWorkerAttemptID:     newWorkerAttemptID,
		})
	})
}

// ReclaimAttempt rotates the claim for a redelivered or restarted GENERATING
// attempt without changing its version or technical retry count.
func (s *Service) ReclaimAttempt(ctx context.Context, analysisID pgtype.UUID) (db.AiAnalysis, error) {
	workerAttemptID, err := newUUID()
	if err != nil {
		return db.AiAnalysis{}, fmt.Errorf("generate worker attempt ID: %w", err)
	}
	return s.inTx(ctx, func(q *db.Queries) (db.AiAnalysis, error) {
		if _, err := requireGenerating(ctx, q, analysisID, nil); err != nil {
			return db.AiAnalysis{}, err
		}
		return q.ReclaimGeneratingAnalysis(ctx, db.ReclaimGeneratingAnalysisParams{
			ID:              analysisID,
			WorkerAttemptID: workerAttemptID,
		})
	})
}

// FinalizeCompleted persists a fully schema-valid verified result and promotes
// it to the case's current analysis in the same transaction.
func (s *Service) FinalizeCompleted(
	ctx context.Context,
	analysisID, workerAttemptID pgtype.UUID,
	result ai.CandidateAnalysis,
	vr ai.VerificationResult,
	provenance *ai.AnalysisProvenance,
) (db.AiAnalysis, error) {
	if err := validateProvenance(provenance); err != nil {
		return db.AiAnalysis{}, err
	}
	encoded, err := encodeCandidate(result)
	if err != nil {
		return db.AiAnalysis{}, err
	}
	if vr.Status != ai.VerificationStatusPass && vr.Status != ai.VerificationStatusPassWithWarning {
		return db.AiAnalysis{}, fmt.Errorf("%w: completed verification status must be PASS or PASS_WITH_WARNING", ErrInvalidResult)
	}
	notes, err := vr.VerificationNotesJSON()
	if err != nil {
		return db.AiAnalysis{}, fmt.Errorf("%w: encode verification notes: %v", ErrInvalidResult, err)
	}

	return s.inTx(ctx, func(q *db.Queries) (db.AiAnalysis, error) {
		attempt, err := requireGenerating(ctx, q, analysisID, &workerAttemptID)
		if err != nil {
			return db.AiAnalysis{}, err
		}
		if _, err := q.LockCaseForAnalysis(ctx, attempt.CaseID); err != nil {
			return db.AiAnalysis{}, err
		}
		finalized, err := q.FinalizeAnalysis(ctx, encoded.finalizeParams(
			analysisID, workerAttemptID, "COMPLETED", vr.Status, notes,
		))
		if err != nil {
			return db.AiAnalysis{}, err
		}
		if err := persistProvenance(ctx, q, analysisID, provenance); err != nil {
			return db.AiAnalysis{}, err
		}
		if err := q.SetCaseCurrentAnalysis(ctx, db.SetCaseCurrentAnalysisParams{
			ID:                attempt.CaseID,
			CurrentAnalysisID: analysisID,
		}); err != nil {
			return db.AiAnalysis{}, err
		}
		return finalized, nil
	})
}

// FinalizeFailed records either a pre-result failure with all result fields
// NULL, or a verifier failure retaining the complete schema-valid candidate.
// Failed attempts never update cases.current_analysis_id.
func (s *Service) FinalizeFailed(
	ctx context.Context,
	analysisID, workerAttemptID pgtype.UUID,
	valid *ai.CandidateAnalysis,
	vr *ai.VerificationResult,
	provenance *ai.AnalysisProvenance,
) (db.AiAnalysis, error) {
	var encoded encodedCandidate
	var verificationStatus ai.VerificationStatus
	var notes []byte

	if valid == nil {
		if provenance != nil && (len(provenance.PolicyRefs) > 0 || len(provenance.EvidenceRefs) > 0) {
			return db.AiAnalysis{}, fmt.Errorf("%w: %w: provenance requires a schema-valid candidate", ErrInvalidResult, ai.ErrInvalidProvenance)
		}
		if vr != nil {
			if vr.Status != ai.VerificationStatusFail {
				return db.AiAnalysis{}, fmt.Errorf("%w: failed verification status must be FAIL", ErrInvalidResult)
			}
			verificationStatus = vr.Status
			var err error
			notes, err = vr.VerificationNotesJSON()
			if err != nil {
				return db.AiAnalysis{}, fmt.Errorf("%w: encode verification notes: %v", ErrInvalidResult, err)
			}
		}
	} else {
		if err := validateProvenance(provenance); err != nil {
			return db.AiAnalysis{}, err
		}
		var err error
		encoded, err = encodeCandidate(*valid)
		if err != nil {
			return db.AiAnalysis{}, err
		}
		if vr == nil || vr.Status != ai.VerificationStatusFail {
			return db.AiAnalysis{}, fmt.Errorf("%w: verifier failure requires status FAIL", ErrInvalidResult)
		}
		verificationStatus = vr.Status
		notes, err = vr.VerificationNotesJSON()
		if err != nil {
			return db.AiAnalysis{}, fmt.Errorf("%w: encode verification notes: %v", ErrInvalidResult, err)
		}
	}

	return s.inTx(ctx, func(q *db.Queries) (db.AiAnalysis, error) {
		if _, err := requireGenerating(ctx, q, analysisID, &workerAttemptID); err != nil {
			return db.AiAnalysis{}, err
		}
		finalized, err := q.FinalizeAnalysis(ctx, encoded.finalizeParams(
			analysisID, workerAttemptID, "FAILED", verificationStatus, notes,
		))
		if err != nil {
			return db.AiAnalysis{}, err
		}
		if valid != nil {
			if err := persistProvenance(ctx, q, analysisID, provenance); err != nil {
				return db.AiAnalysis{}, err
			}
		}
		return finalized, nil
	})
}

func (s *Service) History(ctx context.Context, caseID pgtype.UUID) ([]db.AiAnalysis, error) {
	if s == nil || s.database == nil {
		return nil, errors.New("analysis repository database is not configured")
	}
	return db.New(s.database).ListAnalysisHistory(ctx, caseID)
}

// Latest returns the highest allocated version, regardless of terminal status.
func (s *Service) Latest(ctx context.Context, caseID pgtype.UUID) (db.AiAnalysis, error) {
	if s == nil || s.database == nil {
		return db.AiAnalysis{}, errors.New("analysis repository database is not configured")
	}
	return db.New(s.database).GetLatestAnalysisForCase(ctx, caseID)
}

// Current follows cases.current_analysis_id. A NULL pointer returns
// pgx.ErrNoRows through the generated join query.
func (s *Service) Current(ctx context.Context, caseID pgtype.UUID) (db.AiAnalysis, error) {
	if s == nil || s.database == nil {
		return db.AiAnalysis{}, errors.New("analysis repository database is not configured")
	}
	return db.New(s.database).GetCurrentAnalysis(ctx, caseID)
}

func (s *Service) PolicyRefs(ctx context.Context, analysisID pgtype.UUID) ([]db.AnalysisPolicyRef, error) {
	if s == nil || s.database == nil {
		return nil, errors.New("analysis repository database is not configured")
	}
	return db.New(s.database).ListAnalysisPolicyRefs(ctx, analysisID)
}

func (s *Service) EvidenceRefs(ctx context.Context, analysisID pgtype.UUID) ([]db.AnalysisEvidenceRef, error) {
	if s == nil || s.database == nil {
		return nil, errors.New("analysis repository database is not configured")
	}
	return db.New(s.database).ListAnalysisEvidenceRefs(ctx, analysisID)
}

func (s *Service) inTx(
	ctx context.Context,
	fn func(*db.Queries) (db.AiAnalysis, error),
) (db.AiAnalysis, error) {
	if s == nil || s.database == nil {
		return db.AiAnalysis{}, errors.New("analysis repository database is not configured")
	}
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return db.AiAnalysis{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := fn(db.New(tx))
	if err != nil {
		return db.AiAnalysis{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AiAnalysis{}, err
	}
	return result, nil
}

func requireGenerating(
	ctx context.Context,
	q *db.Queries,
	analysisID pgtype.UUID,
	expectedWorkerAttemptID *pgtype.UUID,
) (db.AiAnalysis, error) {
	attempt, err := q.GetAnalysisForUpdate(ctx, analysisID)
	if err != nil {
		return db.AiAnalysis{}, err
	}
	if attempt.Status != "GENERATING" {
		return db.AiAnalysis{}, ErrImmutable
	}
	if expectedWorkerAttemptID != nil && !sameUUID(attempt.WorkerAttemptID, *expectedWorkerAttemptID) {
		return db.AiAnalysis{}, ErrStaleClaim
	}
	return attempt, nil
}

func validateProvenance(provenance *ai.AnalysisProvenance) error {
	if err := provenance.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResult, err)
	}
	return nil
}

func persistProvenance(
	ctx context.Context,
	q *db.Queries,
	analysisID pgtype.UUID,
	provenance *ai.AnalysisProvenance,
) error {
	if provenance == nil {
		return nil
	}
	for _, ref := range provenance.PolicyRefs {
		id, err := newUUID()
		if err != nil {
			return fmt.Errorf("generate analysis policy ref ID: %w", err)
		}
		if _, err := q.CreateAnalysisPolicyRef(ctx, db.CreateAnalysisPolicyRefParams{
			ID:              id,
			AnalysisID:      analysisID,
			PolicyVersionID: ref.PolicyVersionID,
			Section:         ref.Section,
			Excerpt:         ref.Excerpt,
			RelevanceScore:  ref.RelevanceScore,
		}); err != nil {
			return fmt.Errorf("create analysis policy ref: %w", err)
		}
	}
	for _, ref := range provenance.EvidenceRefs {
		id, err := newUUID()
		if err != nil {
			return fmt.Errorf("generate analysis evidence ref ID: %w", err)
		}
		if _, err := q.CreateAnalysisEvidenceRef(ctx, db.CreateAnalysisEvidenceRefParams{
			ID:         id,
			AnalysisID: analysisID,
			EvidenceID: ref.EvidenceID,
			UsageType:  string(ref.UsageType),
		}); err != nil {
			return fmt.Errorf("create analysis evidence ref: %w", err)
		}
	}
	return nil
}

type encodedCandidate struct {
	summary            pgtype.Text
	facts              []byte
	assumptions        []byte
	unknowns           []byte
	riskAnalysis       []byte
	complianceAnalysis []byte
	recommendation     []byte
	alternatives       []byte
	missingInformation []byte
	policyStatus       pgtype.Text
	evidenceQuality    pgtype.Text
	uncertainty        pgtype.Text
}

func encodeCandidate(candidate ai.CandidateAnalysis) (encodedCandidate, error) {
	if missing := candidate.MissingRequiredFields(); len(missing) > 0 {
		return encodedCandidate{}, fmt.Errorf(
			"%w: missing required fields: %s", ErrInvalidResult, strings.Join(missing, ", "),
		)
	}
	if err := candidate.Validate(); err != nil {
		return encodedCandidate{}, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}

	values := []any{
		candidate.Facts,
		candidate.Assumptions,
		candidate.Unknowns,
		candidate.RiskAnalysis,
		candidate.ComplianceAnalysis,
		candidate.Recommendation,
		candidate.Alternatives,
		candidate.MissingInformation,
	}
	encoded := make([][]byte, len(values))
	for i, value := range values {
		var err error
		encoded[i], err = json.Marshal(value)
		if err != nil {
			return encodedCandidate{}, fmt.Errorf("%w: encode candidate field: %v", ErrInvalidResult, err)
		}
	}

	return encodedCandidate{
		summary:            pgtype.Text{String: candidate.Summary, Valid: true},
		facts:              encoded[0],
		assumptions:        encoded[1],
		unknowns:           encoded[2],
		riskAnalysis:       encoded[3],
		complianceAnalysis: encoded[4],
		recommendation:     encoded[5],
		alternatives:       encoded[6],
		missingInformation: encoded[7],
		policyStatus:       pgtype.Text{String: string(candidate.PolicyStatus), Valid: true},
		evidenceQuality:    pgtype.Text{String: string(candidate.EvidenceQuality), Valid: true},
		uncertainty:        pgtype.Text{String: string(candidate.Uncertainty), Valid: true},
	}, nil
}

func (e encodedCandidate) finalizeParams(
	analysisID, workerAttemptID pgtype.UUID,
	status string,
	verificationStatus ai.VerificationStatus,
	verificationNotes []byte,
) db.FinalizeAnalysisParams {
	return db.FinalizeAnalysisParams{
		ID:                 analysisID,
		WorkerAttemptID:    workerAttemptID,
		Status:             status,
		Summary:            e.summary,
		Facts:              e.facts,
		Assumptions:        e.assumptions,
		Unknowns:           e.unknowns,
		RiskAnalysis:       e.riskAnalysis,
		ComplianceAnalysis: e.complianceAnalysis,
		Recommendation:     e.recommendation,
		Alternatives:       e.alternatives,
		MissingInformation: e.missingInformation,
		PolicyStatus:       e.policyStatus,
		EvidenceQuality:    e.evidenceQuality,
		Uncertainty:        e.uncertainty,
		VerificationStatus: pgtype.Text{
			String: string(verificationStatus),
			Valid:  verificationStatus != "",
		},
		VerificationNotes: verificationNotes,
	}
}

func sameUUID(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && a.Bytes == b.Bytes
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
