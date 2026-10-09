// Package reanalysis atomically persists governed workflow feedback and queues
// the next AI analysis through the transactional outbox.
package reanalysis

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
	"github.com/jawir-team/jawir-sentinel-be/internal/config"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	DefaultMaxReanalysis = config.DefaultMaxReanalysis

	analysisStatusGenerating = "GENERATING"
	auditAnalysisStarted     = "AI_ANALYSIS_STARTED"
	auditReanalysisLimit     = "REANALYSIS_LIMIT_REACHED"
	outboxAnalysisRequested  = "AI_ANALYSIS_REQUESTED"
)

var (
	ErrNotConfigured   = errors.New("reanalysis service is not configured")
	ErrInvalidRequest  = errors.New("invalid reanalysis request")
	ErrMissingAnalysis = errors.New("case has no analysis to reanalyze")
)

// Database is implemented by *pgxpool.Pool and pgx.Tx.
type Database interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Queries is the narrow sqlc boundary used by the orchestration transaction.
type Queries interface {
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	UpdateCaseStatus(context.Context, db.UpdateCaseStatusParams) (db.Case, error)
	LockAnalysisVersionSeq(context.Context, pgtype.UUID) error
	MaxAnalysisVersion(context.Context, pgtype.UUID) (int32, error)
	CreateAnalysis(context.Context, db.CreateAnalysisParams) (db.AiAnalysis, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
	CreateOutboxEvent(context.Context, db.CreateOutboxEventParams) (db.OutboxEvent, error)
}

var _ Queries = (*db.Queries)(nil)

// PersistAction writes the triggering decision, feedback, or execution result.
// The supplied DBTX is the exact transaction used by the orchestrator; callers
// can construct db.New(tx) and use any generated query needed by the action.
type PersistAction func(context.Context, db.DBTX, db.Case) error

type Request struct {
	CaseID        pgtype.UUID
	Trigger       workflow.Event
	ActorID       pgtype.UUID
	ActorRole     string
	ModelName     string
	PromptVersion string
}

type Outcome string

const (
	Queued       Outcome = "QUEUED"
	LimitReached Outcome = "LIMIT_REACHED"
)

type Result struct {
	Outcome  Outcome
	Case     db.Case
	Analysis *db.AiAnalysis
	Outbox   *db.OutboxEvent
}

// TransactionRunner supplies the atomic persistence boundary used by Service.
// It is exported so hermetic integration suites can run the real orchestration
// logic while replacing only PostgreSQL.
type TransactionRunner interface {
	Run(context.Context, func(context.Context, Queries, db.DBTX) error) error
}

type Service struct {
	runner        TransactionRunner
	maxReanalysis int32
}

// NewWithRunner constructs a service with an explicit transaction boundary
// and quota. It is primarily useful for integration tests and alternate
// durable stores; maxReanalysis must be non-negative.
func NewWithRunner(runner TransactionRunner, maxReanalysis int32) *Service {
	return &Service{runner: runner, maxReanalysis: maxReanalysis}
}

// New creates an orchestrator with the startup-validated re-analysis quota.
// Zero intentionally disables all re-analysis cycles.
func New(database Database, maxReanalysis int32) *Service {
	return &Service{
		runner:        pgxTransactionRunner{database: database},
		maxReanalysis: maxReanalysis,
	}
}

// Run persists the triggering business action and its workflow consequences
// in one transaction. It never calls an AI provider or starts a goroutine.
func (s *Service) Run(ctx context.Context, request Request, persist PersistAction) (Result, error) {
	if s == nil || s.runner == nil {
		return Result{}, ErrNotConfigured
	}
	if !validUUID(request.CaseID) || !supportedTrigger(request.Trigger) || persist == nil || s.maxReanalysis < 0 {
		return Result{}, ErrInvalidRequest
	}

	var result Result
	err := s.runner.Run(ctx, func(ctx context.Context, q Queries, tx db.DBTX) error {
		storedCase, err := q.GetCaseForUpdate(ctx, request.CaseID)
		if err != nil {
			return fmt.Errorf("lock case for reanalysis: %w", err)
		}
		if err := persist(ctx, tx, storedCase); err != nil {
			return fmt.Errorf("persist reanalysis trigger: %w", err)
		}

		analysisState, err := workflow.Transition(workflow.State(storedCase.Status), request.Trigger)
		if err != nil {
			return fmt.Errorf("apply reanalysis trigger: %w", err)
		}
		transitioned, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{
			ID: request.CaseID, Status: string(analysisState),
		})
		if err != nil {
			return fmt.Errorf("enter AI analysis: %w", err)
		}

		if err := q.LockAnalysisVersionSeq(ctx, request.CaseID); err != nil {
			return fmt.Errorf("lock analysis version sequence: %w", err)
		}
		latestVersion, err := q.MaxAnalysisVersion(ctx, request.CaseID)
		if err != nil {
			return fmt.Errorf("read latest analysis version: %w", err)
		}
		if latestVersion < 1 || !storedCase.CurrentAnalysisID.Valid {
			return ErrMissingAnalysis
		}

		if latestVersion-1 >= s.maxReanalysis {
			result, err = s.reachLimit(ctx, q, request, storedCase, transitioned, latestVersion)
			return err
		}
		if latestVersion == math.MaxInt32 {
			return errors.New("analysis version limit reached")
		}
		result, err = s.queue(ctx, q, request, transitioned, latestVersion+1)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) reachLimit(
	ctx context.Context,
	q Queries,
	request Request,
	storedCase, transitioned db.Case,
	latestVersion int32,
) (Result, error) {
	metadata, err := json.Marshal(struct {
		LatestAnalysisVersion int32 `json:"latest_analysis_version"`
		MaxReanalysis         int32 `json:"max_reanalysis"`
	}{
		LatestAnalysisVersion: latestVersion,
		MaxReanalysis:         s.maxReanalysis,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode reanalysis limit audit metadata: %w", err)
	}
	if err := appendAudit(ctx, q, request, auditReanalysisLimit, storedCase.CurrentAnalysisID, metadata); err != nil {
		return Result{}, err
	}

	escalationState, err := workflow.Transition(workflow.State(transitioned.Status), workflow.EventReanalysisLimitReached)
	if err != nil {
		return Result{}, fmt.Errorf("apply reanalysis limit: %w", err)
	}
	escalated, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{
		ID: request.CaseID, Status: string(escalationState),
	})
	if err != nil {
		return Result{}, fmt.Errorf("escalate reanalysis limit: %w", err)
	}
	return Result{Outcome: LimitReached, Case: escalated}, nil
}

func (s *Service) queue(
	ctx context.Context,
	q Queries,
	request Request,
	transitioned db.Case,
	version int32,
) (Result, error) {
	analysisID, err := newUUID()
	if err != nil {
		return Result{}, fmt.Errorf("generate analysis ID: %w", err)
	}
	analysis, err := q.CreateAnalysis(ctx, db.CreateAnalysisParams{
		ID:            analysisID,
		CaseID:        request.CaseID,
		Version:       version,
		Status:        analysisStatusGenerating,
		ModelName:     request.ModelName,
		PromptVersion: request.PromptVersion,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create reanalysis version: %w", err)
	}

	metadata, err := json.Marshal(struct {
		Version int32 `json:"version"`
	}{Version: version})
	if err != nil {
		return Result{}, fmt.Errorf("encode analysis started audit metadata: %w", err)
	}
	if err := appendAudit(ctx, q, request, auditAnalysisStarted, analysis.ID, metadata); err != nil {
		return Result{}, err
	}

	payload, err := json.Marshal(struct {
		CaseID     string `json:"case_id"`
		AnalysisID string `json:"analysis_id"`
		Version    int32  `json:"version"`
	}{
		CaseID: request.CaseID.String(), AnalysisID: analysis.ID.String(), Version: version,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode analysis request payload: %w", err)
	}
	outboxID, err := newUUID()
	if err != nil {
		return Result{}, fmt.Errorf("generate outbox event ID: %w", err)
	}
	outbox, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		ID:         outboxID,
		CaseID:     request.CaseID,
		AnalysisID: analysis.ID,
		EventType:  outboxAnalysisRequested,
		Payload:    payload,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create analysis request outbox event: %w", err)
	}

	return Result{Outcome: Queued, Case: transitioned, Analysis: &analysis, Outbox: &outbox}, nil
}

func appendAudit(
	ctx context.Context,
	q Queries,
	request Request,
	eventType string,
	analysisID pgtype.UUID,
	metadata []byte,
) error {
	auditID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate audit event ID: %w", err)
	}
	actorRole := pgtype.Text{}
	if role := strings.TrimSpace(request.ActorRole); role != "" {
		actorRole = pgtype.Text{String: role, Valid: true}
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     request.CaseID,
		EventType:  eventType,
		ActorID:    request.ActorID,
		ActorRole:  actorRole,
		AnalysisID: analysisID,
		Metadata:   metadata,
	}); err != nil {
		return fmt.Errorf("append %s audit event: %w", eventType, err)
	}
	return nil
}

type pgxTransactionRunner struct {
	database Database
}

func (r pgxTransactionRunner) Run(
	ctx context.Context,
	fn func(context.Context, Queries, db.DBTX) error,
) error {
	if r.database == nil {
		return ErrNotConfigured
	}
	tx, err := r.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reanalysis transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(ctx, db.New(tx), tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reanalysis transaction: %w", err)
	}
	return nil
}

func supportedTrigger(event workflow.Event) bool {
	switch event {
	case workflow.EventCheckerRejected,
		workflow.EventSignerRejected,
		workflow.EventExecutionBlocked,
		workflow.EventExecutionFailed:
		return true
	default:
		return false
	}
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
