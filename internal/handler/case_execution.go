package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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
	executionEventStarted = "EXECUTION_STARTED"
	executionInProgress   = "IN_PROGRESS"
)

// ExecutionTxQueries is the database boundary for starting an execution.
type ExecutionTxQueries interface {
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	GetAnalysis(context.Context, pgtype.UUID) (db.AiAnalysis, error)
	ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error)
	ExistsExecutionForCaseAnalysis(context.Context, db.ExistsExecutionForCaseAnalysisParams) (bool, error)
	CreateExecution(context.Context, db.CreateExecutionParams) (db.Execution, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
}

var _ ExecutionTxQueries = (*db.Queries)(nil)

// ExecutionStore starts an execution in one database transaction.
type ExecutionStore interface {
	RunExecutionTx(context.Context, func(context.Context, ExecutionTxQueries) error) error
}

type startExecutionRequest struct {
	AnalysisID string `json:"analysis_id"`
}

type startExecutionResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	CaseStatus string `json:"case_status"`
}

// StartExecution starts work on the current signer-approved analysis.
func StartExecution(store ExecutionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("start execution: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request startExecutionRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, participantAPIError(httpapi.CodeInvalidRequest, "Invalid request body.", err))
			return
		}
		analysisID, err := parseUserUUID(request.AnalysisID)
		if err != nil {
			httpapi.WriteError(w, participantAPIError(httpapi.CodeInvalidRequest, "A valid analysis_id is required.", err))
			return
		}

		var execution db.Execution
		var caseStatus string
		var apiErr *httpapi.APIError
		txErr := store.RunExecutionTx(r.Context(), func(ctx context.Context, q ExecutionTxQueries) error {
			execution, caseStatus, apiErr = runStartExecution(ctx, q, caseID, analysisID, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("start execution", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			var wrapped *httpapi.APIError
			if errors.As(txErr, &wrapped) {
				httpapi.WriteError(w, wrapped)
				return
			}
			logging.With(r.Context()).Error("commit execution start", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: startExecutionResponse{
			ID: execution.ID.String(), Status: execution.Status, CaseStatus: caseStatus,
		}})
	}
}

func runStartExecution(
	ctx context.Context,
	q ExecutionTxQueries,
	caseID pgtype.UUID,
	analysisID pgtype.UUID,
	actor auth.User,
) (db.Execution, string, *httpapi.APIError) {
	stored, err := q.GetCaseForUpdate(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, "", participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if stored.Status != string(workflow.StateExecution) {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "", nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	assignedExecuter := false
	for _, participant := range participants {
		if participant.UserID == actor.ID && participant.Status == participantStatusActive && participant.Role == caseRoleExecuter {
			assignedExecuter = true
			break
		}
	}
	if !assignedExecuter {
		return db.Execution{}, "", participantAPIError(httpapi.CodeForbidden, "", nil)
	}
	if stored.OwnerID == actor.ID {
		return db.Execution{}, "", participantAPIError(httpapi.CodeForbidden, "The case owner cannot execute their own case.", nil)
	}
	if !stored.CurrentAnalysisID.Valid || stored.CurrentAnalysisID != analysisID {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Analysis is not the current analysis.", nil)
	}

	analysis, err := q.GetAnalysis(ctx, analysisID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Current analysis does not exist.", nil)
	}
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if analysis.ID != analysisID || analysis.CaseID != caseID {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Analysis is not the current analysis.", nil)
	}

	decisions, err := q.ListDecisionsByAnalysis(ctx, analysisID)
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	hasSignerApproval := false
	for _, decision := range decisions {
		if decision.ActorRole == caseRoleSigner && decision.Decision == checkerDecisionApprove {
			hasSignerApproval = true
			break
		}
	}
	if !hasSignerApproval {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Current analysis has no signer approval.", nil)
	}

	exists, err := q.ExistsExecutionForCaseAnalysis(ctx, db.ExistsExecutionForCaseAnalysisParams{
		CaseID: caseID, AnalysisID: analysisID,
	})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if exists {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Execution has already started for the current analysis.", nil)
	}

	executionID, err := newUnitUUID()
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	execution, err := q.CreateExecution(ctx, db.CreateExecutionParams{
		ID: executionID, CaseID: caseID, AnalysisID: analysisID, ExecuterID: actor.ID, Status: executionInProgress,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "Execution has already started for the current analysis.", err)
		}
		return db.Execution{}, "", participantInternalError(err)
	}

	metadata, err := json.Marshal(struct {
		ExecutionID string `json:"execution_id"`
	}{ExecutionID: execution.ID.String()})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	auditID, err := newUnitUUID()
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     caseID,
		EventType:  executionEventStarted,
		ActorID:    actor.ID,
		ActorRole:  nullableWorkflowActorRole(caseRoleExecuter),
		AnalysisID: analysisID,
		Metadata:   metadata,
	}); err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}

	return execution, stored.Status, nil
}
