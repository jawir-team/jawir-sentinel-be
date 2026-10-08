package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/audit"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	executionEventStarted       = "EXECUTION_STARTED"
	executionEventSuccess       = "EXECUTION_SUCCESS"
	executionResultEvidenceType = "EXECUTION_RESULT"
	caseEventDone               = "CASE_DONE"
	executionInProgress         = "IN_PROGRESS"
	executionSuccess            = "SUCCESS"
	executionBlocked            = "BLOCKED"
	executionFailed             = "FAILED"
)

// ExecutionTxQueries is the database boundary for execution lifecycle changes.
type ExecutionTxQueries interface {
	audit.Queries
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error)
	ExistsExecutionForCaseAnalysis(context.Context, db.ExistsExecutionForCaseAnalysisParams) (bool, error)
	GetExecutionForUpdate(context.Context, pgtype.UUID) (db.Execution, error)
	CreateExecution(context.Context, db.CreateExecutionParams) (db.Execution, error)
	UpdateExecution(context.Context, db.UpdateExecutionParams) (db.Execution, error)
	CreateEvidence(context.Context, db.CreateEvidenceParams) (db.CaseEvidence, error)
	UpdateCaseStatus(context.Context, db.UpdateCaseStatusParams) (db.Case, error)
}

var _ ExecutionTxQueries = (*db.Queries)(nil)

// ExecutionStore applies an execution lifecycle change in one database transaction.
type ExecutionStore interface {
	RunExecutionTx(context.Context, func(context.Context, ExecutionTxQueries) error) error
}

// ExecutionResultDecider delegates blocked and failed execution results to the
// re-analysis orchestrator, which owns their transaction and workflow effects.
type ExecutionResultDecider struct {
	Store      ExecutionStore
	Reanalysis ReanalysisOrchestrator
}

type startExecutionRequest struct {
	AnalysisID string `json:"analysis_id"`
}

type startExecutionResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	CaseStatus string `json:"case_status"`
}

type finalizeExecutionSuccessRequest struct {
	ActionTaken string `json:"action_taken"`
	Result      string `json:"result"`
}

type finalizeExecutionSuccessResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	CaseStatus string `json:"case_status"`
}

type finalizeExecutionResultRequest struct {
	Outcome     string `json:"outcome"`
	ActionTaken string `json:"action_taken"`
	Result      string `json:"result"`
	Blocker     string `json:"blocker"`
}

type finalizeExecutionResultResponse struct {
	ExecutionID string `json:"execution_id"`
	Outcome     string `json:"outcome"`
	CaseStatus  string `json:"case_status"`
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
	if apiErr := requireCurrentAnalysis(stored, analysisID); apiErr != nil {
		return db.Execution{}, "", apiErr
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
	if _, err := audit.AppendCaseEvent(ctx, q, audit.CaseEvent{
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

// FinalizeExecutionSuccess records a successful execution and completes its case.
func FinalizeExecutionSuccess(store ExecutionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("finalize execution success: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		executionID, err := parseUserUUID(chi.URLParam(r, "execution_id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid execution ID is required.")
			return
		}
		var request finalizeExecutionSuccessRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, participantAPIError(httpapi.CodeInvalidRequest, "Invalid request body.", err))
			return
		}

		var execution db.Execution
		var caseStatus string
		var apiErr *httpapi.APIError
		txErr := store.RunExecutionTx(r.Context(), func(ctx context.Context, q ExecutionTxQueries) error {
			execution, caseStatus, apiErr = runFinalizeExecutionSuccess(ctx, q, caseID, executionID, actor, request)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("finalize execution success", "case_id", caseID.String(), "execution_id", executionID.String(), "error", apiErr)
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
			logging.With(r.Context()).Error("commit execution success", "case_id", caseID.String(), "execution_id", executionID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: finalizeExecutionSuccessResponse{
			ID: execution.ID.String(), Status: execution.Status, CaseStatus: caseStatus,
		}})
	}
}

func runFinalizeExecutionSuccess(
	ctx context.Context,
	q ExecutionTxQueries,
	caseID pgtype.UUID,
	executionID pgtype.UUID,
	actor auth.User,
	request finalizeExecutionSuccessRequest,
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

	execution, err := q.GetExecutionForUpdate(ctx, executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, "", participantAPIError(httpapi.CodeCaseNotFound, "Execution not found.", nil)
	}
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if execution.CaseID != caseID {
		return db.Execution{}, "", participantAPIError(httpapi.CodeCaseNotFound, "Execution not found.", nil)
	}
	if apiErr := requireCurrentAnalysis(stored, execution.AnalysisID); apiErr != nil {
		return db.Execution{}, "", apiErr
	}
	if execution.Status != executionInProgress {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "execution already finalized", nil)
	}
	if actor.ID != execution.ExecuterID {
		return db.Execution{}, "", participantAPIError(httpapi.CodeForbidden, "", nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	activeExecuter := false
	for _, participant := range participants {
		if participant.UserID == actor.ID && participant.Status == participantStatusActive && participant.Role == caseRoleExecuter {
			activeExecuter = true
			break
		}
	}
	if !activeExecuter {
		return db.Execution{}, "", participantAPIError(httpapi.CodeForbidden, "", nil)
	}

	actionTaken := strings.TrimSpace(request.ActionTaken)
	if actionTaken == "" {
		return db.Execution{}, "", participantAPIError(httpapi.CodeValidationError, "Action taken is required.", nil)
	}
	result := strings.TrimSpace(request.Result)
	if result == "" {
		return db.Execution{}, "", participantAPIError(httpapi.CodeValidationError, "Result is required.", nil)
	}

	execution, err = q.UpdateExecution(ctx, db.UpdateExecutionParams{
		ID:          executionID,
		Status:      executionSuccess,
		ActionTaken: pgtype.Text{String: actionTaken, Valid: true},
		Result:      pgtype.Text{String: result, Valid: true},
		Blocker:     pgtype.Text{},
		CompletedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}

	metadata, err := json.Marshal(struct {
		ExecutionID string `json:"execution_id"`
		ActionTaken string `json:"action_taken"`
	}{ExecutionID: execution.ID.String(), ActionTaken: actionTaken})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if apiErr := appendExecutionAudit(ctx, q, caseID, execution.AnalysisID, actor, executionEventSuccess, metadata); apiErr != nil {
		return db.Execution{}, "", apiErr
	}

	next, err := workflow.Transition(workflow.StateExecution, workflow.EventExecutionSuccess)
	if workflow.IsInvalidTransition(err) {
		return db.Execution{}, "", participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
	}
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	updatedCase, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{ID: caseID, Status: string(next)})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	// DONE is terminal in the workflow, so the case is read-only after commit.
	caseStatus := updatedCase.Status

	doneMetadata, err := json.Marshal(struct {
		ExecutionID string `json:"execution_id"`
	}{ExecutionID: execution.ID.String()})
	if err != nil {
		return db.Execution{}, "", participantInternalError(err)
	}
	if apiErr := appendExecutionAudit(ctx, q, caseID, pgtype.UUID{}, actor, caseEventDone, doneMetadata); apiErr != nil {
		return db.Execution{}, "", apiErr
	}

	return execution, caseStatus, nil
}

func appendExecutionAudit(
	ctx context.Context,
	q ExecutionTxQueries,
	caseID pgtype.UUID,
	analysisID pgtype.UUID,
	actor auth.User,
	eventType string,
	metadata []byte,
) *httpapi.APIError {
	auditID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     caseID,
		EventType:  eventType,
		ActorID:    actor.ID,
		ActorRole:  nullableWorkflowActorRole(caseRoleExecuter),
		AnalysisID: analysisID,
		Metadata:   metadata,
	}); err != nil {
		return participantInternalError(err)
	}
	return nil
}

// FinalizeExecutionResult records a blocked or failed execution and delegates
// the resulting re-analysis transition to the re-analysis orchestrator.
func FinalizeExecutionResult(decider ExecutionResultDecider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if decider.Store == nil || isNilInterface(decider.Store) || decider.Reanalysis == nil || isNilInterface(decider.Reanalysis) {
			logging.With(r.Context()).Error("finalize execution result: dependencies are not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		executionID, err := parseUserUUID(chi.URLParam(r, "execution_id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid execution ID is required.")
			return
		}
		var request finalizeExecutionResultRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, participantAPIError(httpapi.CodeInvalidRequest, "Invalid request body.", err))
			return
		}
		request.Outcome = strings.ToUpper(strings.TrimSpace(request.Outcome))
		if request.Outcome != executionBlocked && request.Outcome != executionFailed {
			httpapi.WriteError(w, participantAPIError(httpapi.CodeValidationError, "Outcome must be BLOCKED or FAILED.", nil))
			return
		}

		var execution db.Execution
		caseStatus, apiErr := recordExecutionResult(r.Context(), decider.Reanalysis, caseID, executionID, actor, request, &execution)
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("finalize execution result", "case_id", caseID.String(), "execution_id", executionID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: finalizeExecutionResultResponse{
			ExecutionID: execution.ID.String(), Outcome: execution.Status, CaseStatus: caseStatus,
		}})
	}
}

func recordExecutionResult(
	ctx context.Context,
	orchestrator ReanalysisOrchestrator,
	caseID pgtype.UUID,
	executionID pgtype.UUID,
	actor auth.User,
	request finalizeExecutionResultRequest,
	execution *db.Execution,
) (string, *httpapi.APIError) {
	trigger := workflow.EventExecutionBlocked
	if request.Outcome == executionFailed {
		trigger = workflow.EventExecutionFailed
	}

	result, err := orchestrator.Run(ctx, reanalysis.Request{
		CaseID: caseID, Trigger: trigger, ActorID: actor.ID, ActorRole: caseRoleExecuter,
	}, func(ctx context.Context, tx db.DBTX, _ db.Case) error {
		q := executionResultQueries(tx)
		created, apiErr := persistExecutionResult(ctx, q, caseID, executionID, actor, request)
		if apiErr != nil {
			return apiErr
		}
		*execution = created
		return nil
	})
	if err != nil {
		var apiErr *httpapi.APIError
		if errors.As(err, &apiErr) {
			return "", apiErr
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return "", participantAPIError(httpapi.CodeCaseNotFound, "", err)
		}
		if workflow.IsInvalidTransition(err) {
			return "", participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
		}
		return "", participantInternalError(err)
	}
	if result.Outcome != reanalysis.Queued && result.Outcome != reanalysis.LimitReached {
		return "", participantInternalError(errors.New("reanalysis returned an unknown outcome"))
	}
	return result.Case.Status, nil
}

func executionResultQueries(tx db.DBTX) ExecutionTxQueries {
	if q, ok := tx.(ExecutionTxQueries); ok {
		return q
	}
	return db.New(tx)
}

func persistExecutionResult(
	ctx context.Context,
	q ExecutionTxQueries,
	caseID pgtype.UUID,
	executionID pgtype.UUID,
	actor auth.User,
	request finalizeExecutionResultRequest,
) (db.Execution, *httpapi.APIError) {
	stored, err := q.GetCaseForUpdate(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.Execution{}, participantInternalError(err)
	}
	if stored.Status != string(workflow.StateExecution) {
		return db.Execution{}, participantAPIError(httpapi.CodeInvalidStateTransition, "", nil)
	}

	execution, err := q.GetExecutionForUpdate(ctx, executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, participantAPIError(httpapi.CodeCaseNotFound, "Execution not found.", nil)
	}
	if err != nil {
		return db.Execution{}, participantInternalError(err)
	}
	if execution.CaseID != caseID {
		return db.Execution{}, participantAPIError(httpapi.CodeCaseNotFound, "Execution not found.", nil)
	}
	if apiErr := requireCurrentAnalysis(stored, execution.AnalysisID); apiErr != nil {
		return db.Execution{}, apiErr
	}
	if execution.Status != executionInProgress {
		return db.Execution{}, participantAPIError(httpapi.CodeInvalidStateTransition, "execution already finalized", nil)
	}
	if actor.ID != execution.ExecuterID {
		return db.Execution{}, participantAPIError(httpapi.CodeForbidden, "", nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.Execution{}, participantInternalError(err)
	}
	actorRole, active := participantActorRole(participants, actor)
	if !active || actorRole != caseRoleExecuter {
		return db.Execution{}, participantAPIError(httpapi.CodeForbidden, "", nil)
	}

	actionTaken := strings.TrimSpace(request.ActionTaken)
	result := strings.TrimSpace(request.Result)
	blocker := strings.TrimSpace(request.Blocker)
	if blocker == "" {
		return db.Execution{}, participantAPIError(httpapi.CodeValidationError, "Blocker is required.", nil)
	}
	if request.Outcome == executionFailed && actionTaken == "" {
		return db.Execution{}, participantAPIError(httpapi.CodeValidationError, "Action taken is required for a failed execution.", nil)
	}
	if request.Outcome == executionFailed && result == "" {
		return db.Execution{}, participantAPIError(httpapi.CodeValidationError, "Result is required for a failed execution.", nil)
	}

	execution, err = q.UpdateExecution(ctx, db.UpdateExecutionParams{
		ID:          executionID,
		Status:      request.Outcome,
		ActionTaken: nullableExecutionText(actionTaken),
		Result:      nullableExecutionText(result),
		Blocker:     pgtype.Text{String: blocker, Valid: true},
		CompletedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return db.Execution{}, participantInternalError(err)
	}

	if apiErr := createExecutionResultEvidence(ctx, q, caseID, actor, actorRole, request.Outcome, actionTaken, result, blocker); apiErr != nil {
		return db.Execution{}, apiErr
	}
	metadata, err := json.Marshal(struct {
		ExecutionID string `json:"execution_id"`
		Outcome     string `json:"outcome"`
	}{ExecutionID: execution.ID.String(), Outcome: request.Outcome})
	if err != nil {
		return db.Execution{}, participantInternalError(err)
	}
	auditEvent := string(workflow.EventExecutionBlocked)
	if request.Outcome == executionFailed {
		auditEvent = string(workflow.EventExecutionFailed)
	}
	if apiErr := appendExecutionAudit(ctx, q, caseID, execution.AnalysisID, actor, auditEvent, metadata); apiErr != nil {
		return db.Execution{}, apiErr
	}

	return execution, nil
}

func createExecutionResultEvidence(
	ctx context.Context,
	q ExecutionTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	actorRole string,
	outcome string,
	actionTaken string,
	result string,
	blocker string,
) *httpapi.APIError {
	evidenceID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	contentParts := make([]string, 0, 3)
	if actionTaken != "" {
		contentParts = append(contentParts, "Action taken: "+actionTaken)
	}
	if result != "" {
		contentParts = append(contentParts, "Result: "+result)
	}
	contentParts = append(contentParts, "Blocker: "+blocker)
	if _, err := q.CreateEvidence(ctx, db.CreateEvidenceParams{
		ID:           evidenceID,
		CaseID:       caseID,
		SourceType:   actorRole,
		SourceUserID: actor.ID,
		EvidenceType: executionResultEvidenceType,
		Title:        pgtype.Text{String: "Execution " + outcome, Valid: true},
		Content:      pgtype.Text{String: strings.Join(contentParts, "\n"), Valid: true},
		FilePath:     pgtype.Text{},
		MimeType:     pgtype.Text{},
	}); err != nil {
		return participantInternalError(err)
	}
	return nil
}

func nullableExecutionText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
