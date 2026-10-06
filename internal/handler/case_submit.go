package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	caseEventSubmitted        = "CASE_SUBMITTED"
	analysisEventStarted      = "AI_ANALYSIS_STARTED"
	analysisStatusGenerating  = "GENERATING"
	outboxEventAnalysisNeeded = "AI_ANALYSIS_REQUESTED"
)

// SubmitCaseStore runs the complete case submission workflow atomically.
type SubmitCaseStore interface {
	RunSubmitTx(context.Context, func(context.Context, SubmitTxQueries) error) error
}

// SubmitCase validates and freezes a draft case, then queues its analysis.
// Analysis is asynchronous: the BE-053 worker consumes the durable outbox
// event, so this request path makes no network calls and starts no goroutines.
func SubmitCase(store SubmitCaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("submit case: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}

		var submitted *db.Case
		var apiErr *httpapi.APIError
		txErr := store.RunSubmitTx(r.Context(), func(ctx context.Context, q SubmitTxQueries) error {
			submitted, apiErr = runSubmitCase(ctx, q, caseID, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("submit case", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit case submission", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newCaseResponse(*submitted)})
	}
}

func runSubmitCase(ctx context.Context, q SubmitTxQueries, caseID pgtype.UUID, actor auth.User) (*db.Case, *httpapi.APIError) {
	stored, err := q.GetCaseForUpdate(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return nil, participantInternalError(err)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	actorRole, allowed := participantActorRole(participants, actor)
	if !allowed {
		return nil, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}

	submittedState, err := workflow.Transition(workflow.State(stored.Status), workflow.EventSubmit)
	if workflow.IsInvalidTransition(err) {
		return nil, participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
	}
	if err != nil {
		return nil, participantInternalError(err)
	}

	activeMakers := 0
	activeRequiredCheckers := 0
	activeSigners := 0
	activeExecuters := 0
	makerIsOwner := false
	for _, participant := range participants {
		if participant.Status != participantStatusActive {
			continue
		}
		switch participant.Role {
		case caseRoleMaker:
			activeMakers++
			makerIsOwner = participant.UserID == stored.OwnerID
		case caseRoleChecker:
			if participant.Required {
				activeRequiredCheckers++
			}
		case caseRoleSigner:
			activeSigners++
		case caseRoleExecuter:
			activeExecuters++
		}
	}
	if activeMakers != 1 || !makerIsOwner {
		return nil, participantAPIError(httpapi.CodeCardinalityViolation, "Exactly one active MAKER matching the case owner is required.", nil)
	}
	if activeRequiredCheckers < 1 {
		return nil, participantAPIError(httpapi.CodeCardinalityViolation, "At least one active required CHECKER is required.", nil)
	}
	if activeSigners != 1 {
		return nil, participantAPIError(httpapi.CodeCardinalityViolation, "Exactly one active SIGNER is required.", nil)
	}
	if activeExecuters != 1 {
		return nil, participantAPIError(httpapi.CodeCardinalityViolation, "Exactly one active EXECUTER is required.", nil)
	}
	if apiErr := ValidateCaseSoD(participants); apiErr != nil {
		return nil, apiErr
	}
	if strings.TrimSpace(stored.Title) == "" {
		return nil, participantAPIError(httpapi.CodeValidationError, "Case title is required.", nil)
	}

	finalState, err := workflow.Transition(submittedState, workflow.EventStartAnalysis)
	if err != nil {
		return nil, participantInternalError(err)
	}
	// Persisting AI_ANALYSIS is the freeze: UpdateCase and all participant
	// mutations require DRAFT, so core data and participants become immutable
	// automatically without a migration or a new column.
	updated, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{
		ID:     caseID,
		Status: string(finalState),
	})
	if err != nil {
		return nil, participantInternalError(err)
	}

	analysisID, err := newUnitUUID()
	if err != nil {
		return nil, participantInternalError(err)
	}
	// Empty placeholders are required because model_name and prompt_version are
	// NOT NULL with no defaults. The BE-053 worker sets the real values when it
	// picks up the outbox event.
	analysis, err := q.CreateAnalysis(ctx, db.CreateAnalysisParams{
		ID:            analysisID,
		CaseID:        caseID,
		Version:       1,
		Status:        analysisStatusGenerating,
		ModelName:     "",
		PromptVersion: "",
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, participantAPIError(httpapi.CodeConflict, "An analysis already exists for this case version.", err)
		}
		return nil, participantInternalError(err)
	}

	if apiErr := appendSubmitAuditEvent(ctx, q, caseID, actor, actorRole, caseEventSubmitted, pgtype.UUID{}, []byte(`{}`)); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := appendSubmitAuditEvent(ctx, q, caseID, actor, actorRole, analysisEventStarted, analysis.ID, []byte(`{"version":1}`)); apiErr != nil {
		return nil, apiErr
	}

	payload, err := json.Marshal(struct {
		CaseID     string `json:"case_id"`
		AnalysisID string `json:"analysis_id"`
		Version    int32  `json:"version"`
	}{
		CaseID:     caseID.String(),
		AnalysisID: analysis.ID.String(),
		Version:    1,
	})
	if err != nil {
		return nil, participantInternalError(err)
	}
	outboxID, err := newUnitUUID()
	if err != nil {
		return nil, participantInternalError(err)
	}
	if _, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		ID:         outboxID,
		CaseID:     caseID,
		AnalysisID: analysis.ID,
		EventType:  outboxEventAnalysisNeeded,
		Payload:    payload,
	}); err != nil {
		if isUniqueViolation(err) {
			return nil, participantAPIError(httpapi.CodeConflict, "An analysis request already exists.", err)
		}
		return nil, participantInternalError(err)
	}

	return &updated, nil
}

func appendSubmitAuditEvent(
	ctx context.Context,
	q SubmitTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	actorRole, eventType string,
	analysisID pgtype.UUID,
	metadata []byte,
) *httpapi.APIError {
	auditID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	_, err = q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     caseID,
		EventType:  eventType,
		ActorID:    actor.ID,
		ActorRole:  nullableWorkflowActorRole(actorRole),
		AnalysisID: analysisID,
		Metadata:   metadata,
	})
	if err != nil {
		return participantInternalError(err)
	}
	return nil
}
