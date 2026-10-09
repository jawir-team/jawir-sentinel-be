// Package aiconsumer validates durable AI messages, claims analysis attempts,
// and routes all terminal mutations through aiworker's fenced operations.
package aiconsumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/rabbitmq"
)

const EventAnalysisRequested = "AI_ANALYSIS_REQUESTED"

var ErrExecutorNotConfigured = errors.New("AI orchestration executor is not configured")

// Worker is the fenced mutation surface supplied by internal/aiworker.
type Worker interface {
	Claim(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) (aiworker.ClaimResult, error)
	FinalizeSuccess(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, aiworker.SuccessResult) (aiworker.Outcome, error)
	VerifierFail(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, aiworker.FailedCandidate) (aiworker.Outcome, error)
	TechnicalExhaustion(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) (aiworker.Outcome, error)
	TechnicalRetry(context.Context, pgtype.UUID, pgtype.UUID) (db.AiAnalysis, error)
}

// Executor performs only external/read-side orchestration. It must not write
// ai_analyses; Processor owns all fenced finalization.
type Executor interface {
	Execute(context.Context, Job) (Execution, error)
}

type Job struct {
	CaseID          pgtype.UUID
	AnalysisID      pgtype.UUID
	OutboxEventID   pgtype.UUID
	WorkerAttemptID pgtype.UUID
}

// Execution must contain exactly one semantic result.
type Execution struct {
	Success         *aiworker.SuccessResult
	VerifierFailure *aiworker.FailedCandidate
}

type Processor struct {
	worker              Worker
	executor            Executor
	maxTechnicalRetries int32
}

func New(worker Worker, executor Executor, maxTechnicalRetries int) *Processor {
	if maxTechnicalRetries < 0 {
		maxTechnicalRetries = 0
	}
	return &Processor{worker: worker, executor: executor, maxTechnicalRetries: int32(maxTechnicalRetries)}
}

// Handle is suitable for rabbitmq.Broker.Consume. A nil return is the sole ACK
// signal and is produced only for duplicates/terminal jobs or after a durable
// fenced finalization.
func (p *Processor) Handle(ctx context.Context, message rabbitmq.Message) error {
	if p == nil || p.worker == nil {
		return errors.New("AI consumer worker is not configured")
	}
	// Do not claim work that this process cannot execute; the broker delivery
	// stays available for a correctly configured worker.
	if p.executor == nil {
		return ErrExecutorNotConfigured
	}
	payload, err := decodeMessage(message)
	if err != nil {
		return err
	}

	claim, err := p.worker.Claim(ctx, payload.caseID, payload.analysisID, payload.outboxEventID)
	if err != nil {
		return err
	}
	switch claim.Outcome {
	case aiworker.AlreadyFinalized, aiworker.Duplicate:
		return nil
	case aiworker.Claimed:
	default:
		return fmt.Errorf("AI consumer received unknown claim outcome %q", claim.Outcome)
	}

	attemptID := claim.WorkerAttemptID
	retryCount := claim.TechnicalRetryCount
	for {
		result, executionErr := p.executor.Execute(ctx, Job{
			CaseID: payload.caseID, AnalysisID: payload.analysisID,
			OutboxEventID: payload.outboxEventID, WorkerAttemptID: attemptID,
		})
		if executionErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if retryCount >= p.maxTechnicalRetries {
				_, err := p.worker.TechnicalExhaustion(ctx, payload.caseID, payload.analysisID, attemptID)
				return err
			}
			retried, err := p.worker.TechnicalRetry(ctx, payload.analysisID, attemptID)
			if err != nil {
				return err
			}
			attemptID = retried.WorkerAttemptID
			retryCount = retried.TechnicalRetryCount
			continue
		}

		switch {
		case result.Success != nil && result.VerifierFailure == nil:
			_, err := p.worker.FinalizeSuccess(ctx, payload.caseID, payload.analysisID, attemptID, *result.Success)
			return err
		case result.VerifierFailure != nil && result.Success == nil:
			_, err := p.worker.VerifierFail(ctx, payload.caseID, payload.analysisID, attemptID, *result.VerifierFailure)
			return err
		default:
			return errors.New("AI executor returned an invalid result")
		}
	}
}

type wirePayload struct {
	CaseID        string `json:"case_id"`
	AnalysisID    string `json:"analysis_id"`
	OutboxEventID string `json:"outbox_event_id"`
	EventType     string `json:"event_type"`
}

type decodedPayload struct {
	caseID        pgtype.UUID
	analysisID    pgtype.UUID
	outboxEventID pgtype.UUID
}

func decodeMessage(message rabbitmq.Message) (decodedPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(message.Body))
	decoder.DisallowUnknownFields()
	var wire wirePayload
	if err := decoder.Decode(&wire); err != nil {
		return decodedPayload{}, errors.New("AI delivery payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return decodedPayload{}, errors.New("AI delivery payload contains trailing data")
	}
	if wire.EventType != EventAnalysisRequested {
		return decodedPayload{}, errors.New("AI delivery event type is invalid")
	}
	caseID, err := parseUUID(wire.CaseID)
	if err != nil {
		return decodedPayload{}, errors.New("AI delivery case ID is invalid")
	}
	analysisID, err := parseUUID(wire.AnalysisID)
	if err != nil {
		return decodedPayload{}, errors.New("AI delivery analysis ID is invalid")
	}
	outboxID, err := parseUUID(wire.OutboxEventID)
	if err != nil {
		return decodedPayload{}, errors.New("AI delivery outbox event ID is invalid")
	}
	if message.MessageID != wire.OutboxEventID {
		return decodedPayload{}, errors.New("AI delivery message ID does not match its outbox event ID")
	}
	return decodedPayload{caseID: caseID, analysisID: analysisID, outboxEventID: outboxID}, nil
}

func parseUUID(value string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return pgtype.UUID{}, errors.New("invalid UUID")
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}
