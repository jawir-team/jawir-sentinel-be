package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

// HistoryStore is the read-only database boundary for case audit history.
// *db.Queries and *TxQueries both satisfy it.
type HistoryStore interface {
	GetCase(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ListCaseAuditEvents(context.Context, pgtype.UUID) ([]db.AuditEvent, error)
	GetAnalysis(context.Context, pgtype.UUID) (db.AiAnalysis, error)
}

var _ HistoryStore = (*db.Queries)(nil)

type caseHistoryResponse struct {
	EventID         pgtype.UUID     `json:"event_id"`
	EventType       string          `json:"event_type"`
	CreatedAt       time.Time       `json:"created_at"`
	ActorID         *pgtype.UUID    `json:"actor_id"`
	ActorRole       *string         `json:"actor_role"`
	AnalysisID      *pgtype.UUID    `json:"analysis_id"`
	AnalysisVersion *int32          `json:"analysis_version"`
	Metadata        json.RawMessage `json:"metadata"`
}

// GetCaseHistory returns the immutable CASE-scope audit timeline in storage
// order. The backing query orders events chronologically by created_at and id.
func GetCaseHistory(store HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("get case history: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}

		if _, err := store.GetCase(r.Context(), caseID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeCaseNotFound(w)
				return
			}
			logging.With(r.Context()).Error("get case for history", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		if actor.SystemRole != auth.SystemRoleAdmin {
			participants, err := store.ListCaseParticipants(r.Context(), caseID)
			if err != nil {
				logging.With(r.Context()).Error("list case participants for history", "case_id", caseID.String(), "error", err)
				httpapi.WriteError(w, nil)
				return
			}
			if !isActiveCaseParticipant(participants, actor.ID) {
				writeCaseNotFound(w)
				return
			}
		}

		events, err := store.ListCaseAuditEvents(r.Context(), caseID)
		if err != nil {
			logging.With(r.Context()).Error("list case audit events", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		response := make([]caseHistoryResponse, 0, len(events))
		analysisVersions := make(map[pgtype.UUID]int32)
		for _, event := range events {
			item := caseHistoryResponse{
				EventID:   event.ID,
				EventType: event.EventType,
				CreatedAt: event.CreatedAt,
				Metadata:  sanitizeHistoryMetadata(event.EventType, event.Metadata),
			}
			if event.ActorID.Valid {
				actorID := event.ActorID
				item.ActorID = &actorID
			}
			if event.ActorRole.Valid {
				actorRole := event.ActorRole.String
				item.ActorRole = &actorRole
			}
			if event.AnalysisID.Valid {
				analysisID := event.AnalysisID
				item.AnalysisID = &analysisID

				version, found := analysisVersions[analysisID]
				if !found {
					analysis, err := store.GetAnalysis(r.Context(), analysisID)
					if err != nil {
						logging.With(r.Context()).Error("get analysis for case history", "case_id", caseID.String(), "analysis_id", analysisID.String(), "error", err)
						httpapi.WriteError(w, nil)
						return
					}
					version = analysis.Version
					analysisVersions[analysisID] = version
				}
				item.AnalysisVersion = &version
			}
			response = append(response, item)
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

var historyMetadataDenylist = map[string]struct{}{
	"prompt":           {},
	"prompt_text":      {},
	"system_prompt":    {},
	"evidence_content": {},
	"content":          {},
	"file_path":        {},
	"stack_trace":      {},
	"stacktrace":       {},
	"raw_payload":      {},
	"raw_response":     {},
	"credentials":      {},
	"credential":       {},
	"token":            {},
	"secret":           {},
	"api_key":          {},
}

var historyDecisionEvents = map[string]struct{}{
	"CHECKER_APPROVED": {},
	"CHECKER_REJECTED": {},
	"SIGNER_APPROVED":  {},
	"SIGNER_REJECTED":  {},
}

// sanitizeHistoryMetadata applies an event-specific allowlist and recursively
// removes sensitive fields. Invalid or non-object metadata becomes an empty
// object so unsafe raw bytes can never reach the response encoder.
func sanitizeHistoryMetadata(eventType string, raw json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return json.RawMessage(`{}`)
	}

	var allowlist map[string]struct{}
	switch {
	case eventType == "AI_ANALYSIS_FAILED":
		allowlist = historyMetadataKeys("failure_type")
	case eventType == "REANALYSIS_LIMIT_REACHED":
		allowlist = historyMetadataKeys("latest_analysis_version", "max_reanalysis")
	case isHistoryDecisionEvent(eventType):
		allowlist = historyMetadataKeys("analysis_id", "decision", "evidence_ids")
	case strings.HasPrefix(eventType, "EXECUTION_"):
		allowlist = historyMetadataKeys("execution_id", "outcome", "action_taken", "result")
	case eventType == "EVIDENCE_ADDED":
		allowlist = historyMetadataKeys("evidence_id", "evidence_type", "title")
	case strings.HasPrefix(eventType, "PARTICIPANT_"):
		allowlist = historyMetadataKeys("user_id", "role")
	}

	filtered := make(map[string]any)
	for key, value := range metadata {
		if _, denied := historyMetadataDenylist[strings.ToLower(key)]; denied {
			continue
		}
		if allowlist != nil {
			if _, allowed := allowlist[key]; !allowed {
				continue
			}
		}
		filtered[key] = sanitizeHistoryMetadataValue(value)
	}

	encoded, err := json.Marshal(filtered)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func historyMetadataKeys(keys ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}

func isHistoryDecisionEvent(eventType string) bool {
	_, ok := historyDecisionEvents[eventType]
	return ok
}

func sanitizeHistoryMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		filtered := make(map[string]any)
		for key, nested := range typed {
			if _, denied := historyMetadataDenylist[strings.ToLower(key)]; denied {
				continue
			}
			filtered[key] = sanitizeHistoryMetadataValue(nested)
		}
		return filtered
	case []any:
		filtered := make([]any, len(typed))
		for index, nested := range typed {
			filtered[index] = sanitizeHistoryMetadataValue(nested)
		}
		return filtered
	default:
		return typed
	}
}
