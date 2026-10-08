package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

// Scope identifies the owner of an audit event.
type Scope string

const (
	ScopeCase   Scope = "CASE"
	ScopePolicy Scope = "POLICY"
)

// CaseEvent contains the caller-controlled fields of a case audit event.
type CaseEvent struct {
	CaseID     pgtype.UUID
	EventType  string
	ActorID    pgtype.UUID
	ActorRole  pgtype.Text
	AnalysisID pgtype.UUID
	Metadata   []byte
}

// PolicyEvent contains the caller-controlled fields of a policy audit event.
// Policy audit events never accept an actor role.
type PolicyEvent struct {
	PolicyID        pgtype.UUID
	PolicyVersionID pgtype.UUID
	EventType       string
	ActorID         pgtype.UUID
	Metadata        []byte
}

// Queries is the complete database surface used by the scoped audit service.
type Queries interface {
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
	AppendPolicyAuditEvent(context.Context, db.AppendPolicyAuditEventParams) (db.AuditEvent, error)
	GetAnalysis(context.Context, pgtype.UUID) (db.AiAnalysis, error)
	GetPolicyVersion(context.Context, pgtype.UUID) (db.PolicyVersion, error)
}

var _ Queries = (*db.Queries)(nil)

var allowedCaseEvents = map[string]struct{}{
	"CASE_CREATED":             {},
	"CASE_UPDATED":             {},
	"CASE_SUBMITTED":           {},
	"CASE_CLOSED":              {},
	"CASE_DONE":                {},
	"PARTICIPANT_ASSIGNED":     {},
	"PARTICIPANT_UNASSIGNED":   {},
	"EVIDENCE_ADDED":           {},
	"AI_ANALYSIS_STARTED":      {},
	"AI_ANALYSIS_COMPLETED":    {},
	"AI_ANALYSIS_FAILED":       {},
	"REANALYSIS_LIMIT_REACHED": {},
	"CHECKER_APPROVED":         {},
	"CHECKER_REJECTED":         {},
	"SIGNER_APPROVED":          {},
	"SIGNER_REJECTED":          {},
	"EXECUTION_STARTED":        {},
	"EXECUTION_BLOCKED":        {},
	"EXECUTION_FAILED":         {},
	"EXECUTION_SUCCESS":        {},
}

var allowedPolicyEvents = map[string]struct{}{
	"POLICY_CREATED":         {},
	"POLICY_VERSION_CREATED": {},
	"POLICY_ACTIVATED":       {},
	"POLICY_SUPERSEDED":      {},
}

// AppendCaseEvent validates and appends a case-scoped event.
func AppendCaseEvent(ctx context.Context, q Queries, event CaseEvent) (db.AuditEvent, error) {
	if _, ok := allowedCaseEvents[event.EventType]; !ok {
		return db.AuditEvent{}, fmt.Errorf("unknown CASE audit event type %q", event.EventType)
	}
	if event.AnalysisID.Valid {
		analysis, err := q.GetAnalysis(ctx, event.AnalysisID)
		if err != nil {
			return db.AuditEvent{}, fmt.Errorf("get analysis for audit event: %w", err)
		}
		if analysis.CaseID != event.CaseID {
			return db.AuditEvent{}, fmt.Errorf("analysis does not belong to case")
		}
	}
	if err := validateMetadata(event.EventType, event.Metadata); err != nil {
		return db.AuditEvent{}, err
	}
	metadata := normalizedMetadata(event.Metadata)

	return q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         newUUID(),
		CaseID:     event.CaseID,
		EventType:  event.EventType,
		ActorID:    event.ActorID,
		ActorRole:  event.ActorRole,
		AnalysisID: event.AnalysisID,
		Metadata:   metadata,
	})
}

// AppendPolicyEvent validates and appends a policy-scoped event. The generated
// policy insert has no actor_role or case fields, so those columns remain NULL.
func AppendPolicyEvent(ctx context.Context, q Queries, event PolicyEvent) (db.AuditEvent, error) {
	if _, ok := allowedPolicyEvents[event.EventType]; !ok {
		return db.AuditEvent{}, fmt.Errorf("unknown POLICY audit event type %q", event.EventType)
	}
	if event.PolicyVersionID.Valid {
		version, err := q.GetPolicyVersion(ctx, event.PolicyVersionID)
		if err != nil {
			return db.AuditEvent{}, fmt.Errorf("get policy version for audit event: %w", err)
		}
		if version.PolicyID != event.PolicyID {
			return db.AuditEvent{}, fmt.Errorf("policy version does not belong to policy")
		}
	}
	if err := validateMetadata(event.EventType, event.Metadata); err != nil {
		return db.AuditEvent{}, err
	}
	metadata := normalizedMetadata(event.Metadata)

	return q.AppendPolicyAuditEvent(ctx, db.AppendPolicyAuditEventParams{
		ID:              newUUID(),
		PolicyID:        event.PolicyID,
		PolicyVersionID: event.PolicyVersionID,
		EventType:       event.EventType,
		ActorID:         event.ActorID,
		Metadata:        metadata,
	})
}

func validateMetadata(eventType string, metadata []byte) error {
	trimmed := bytes.TrimSpace(metadata)
	if len(trimmed) == 0 {
		switch eventType {
		case "AI_ANALYSIS_FAILED":
			return fmt.Errorf("AI_ANALYSIS_FAILED metadata must contain failure_type")
		case "REANALYSIS_LIMIT_REACHED":
			return fmt.Errorf("REANALYSIS_LIMIT_REACHED metadata must contain latest_analysis_version and max_reanalysis")
		default:
			return nil
		}
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		if err != nil {
			return fmt.Errorf("metadata must be a valid JSON object: %w", err)
		}
		return fmt.Errorf("metadata must be a valid JSON object")
	}

	switch eventType {
	case "AI_ANALYSIS_FAILED":
		var failureType string
		raw, ok := object["failure_type"]
		if !ok || json.Unmarshal(raw, &failureType) != nil {
			return fmt.Errorf("AI_ANALYSIS_FAILED metadata must contain failure_type")
		}
		if failureType != "VERIFIER_FAIL" && failureType != "TECHNICAL_RETRY_EXHAUSTED" {
			return fmt.Errorf("AI_ANALYSIS_FAILED metadata failure_type %q is not allowed", failureType)
		}
	case "REANALYSIS_LIMIT_REACHED":
		if !isJSONNumber(object["latest_analysis_version"]) {
			return fmt.Errorf("REANALYSIS_LIMIT_REACHED metadata latest_analysis_version must be a number")
		}
		if !isJSONNumber(object["max_reanalysis"]) {
			return fmt.Errorf("REANALYSIS_LIMIT_REACHED metadata max_reanalysis must be a number")
		}
	}

	return nil
}

func normalizedMetadata(metadata []byte) []byte {
	if len(bytes.TrimSpace(metadata)) == 0 {
		return []byte(`{}`)
	}
	return metadata
}

func isJSONNumber(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	_, ok := value.(json.Number)
	return ok
}

func newUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}
