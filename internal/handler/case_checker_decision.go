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
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	checkerDecisionApprove       = "APPROVE"
	checkerDecisionReject        = "REJECT"
	checkerEventApproved         = "CHECKER_APPROVED"
	checkerEventRejected         = "CHECKER_REJECTED"
	reviewerFeedbackEvidenceType = "REVIEWER_FEEDBACK"
)

// DecisionTxQueries is the database boundary for recording a checker or signer
// decision. *db.Queries satisfies it both in handler-owned approval
// transactions and in re-analysis-owned rejection transactions.
type DecisionTxQueries interface {
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error)
	ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error)
	CreateDecision(context.Context, db.CreateDecisionParams) (db.Decision, error)
	CreateEvidence(context.Context, db.CreateEvidenceParams) (db.CaseEvidence, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
	UpdateCaseStatus(context.Context, db.UpdateCaseStatusParams) (db.Case, error)
}

var _ DecisionTxQueries = (*db.Queries)(nil)

// CheckerDecisionTxQueries is kept as the public transaction callback type so
// existing stores do not need a second, identical transaction method.
type CheckerDecisionTxQueries = DecisionTxQueries

// CheckerDecisionStore runs an approval in one database transaction.
type CheckerDecisionStore interface {
	RunCheckerDecisionTx(context.Context, func(context.Context, CheckerDecisionTxQueries) error) error
}

// ReanalysisOrchestrator owns the rejection transaction and all re-analysis
// quota/version/outbox behavior.
type ReanalysisOrchestrator interface {
	Run(context.Context, reanalysis.Request, reanalysis.PersistAction) (reanalysis.Result, error)
}

var _ ReanalysisOrchestrator = (*reanalysis.Service)(nil)

type CheckerDecider struct {
	Store      CheckerDecisionStore
	Reanalysis ReanalysisOrchestrator
}

type reviewerDecisionRequest struct {
	AnalysisID  string   `json:"analysis_id"`
	Decision    string   `json:"decision"`
	Comment     string   `json:"comment"`
	Reason      string   `json:"reason"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type parsedReviewerDecision struct {
	analysisID  pgtype.UUID
	decision    string
	comment     string
	reason      string
	evidenceIDs []pgtype.UUID
}

type reviewerDecisionResponse struct {
	DecisionID string `json:"decision_id"`
	Decision   string `json:"decision"`
	CaseStatus string `json:"case_status"`
}

// RecordCheckerDecision records an approval or rejection against the current
// analysis. Rejections delegate transaction ownership to the re-analysis
// orchestrator so the decision and its workflow consequences remain atomic.
func RecordCheckerDecision(decider CheckerDecider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if decider.Store == nil || isNilInterface(decider.Store) || decider.Reanalysis == nil || isNilInterface(decider.Reanalysis) {
			logging.With(r.Context()).Error("record checker decision: dependencies are not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		request, apiErr := parseReviewerDecisionRequest(w, r)
		if apiErr != nil {
			httpapi.WriteError(w, apiErr)
			return
		}

		var decision db.Decision
		var caseStatus string
		if request.decision == checkerDecisionApprove {
			apiErr = recordCheckerApproval(r.Context(), decider.Store, caseID, actor, request, &decision, &caseStatus)
		} else {
			apiErr = recordCheckerRejection(r.Context(), decider.Reanalysis, caseID, actor, request, &decision, &caseStatus)
		}
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("record checker decision", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: reviewerDecisionResponse{
			DecisionID: decision.ID.String(),
			Decision:   decision.Decision,
			CaseStatus: caseStatus,
		}})
	}
}

func parseReviewerDecisionRequest(w http.ResponseWriter, r *http.Request) (parsedReviewerDecision, *httpapi.APIError) {
	var raw reviewerDecisionRequest
	if err := decodeCaseRequest(w, r, &raw); err != nil {
		return parsedReviewerDecision{}, participantAPIError(httpapi.CodeInvalidRequest, "Invalid request body.", err)
	}

	decision := strings.ToUpper(strings.TrimSpace(raw.Decision))
	if decision != checkerDecisionApprove && decision != checkerDecisionReject {
		return parsedReviewerDecision{}, participantAPIError(httpapi.CodeValidationError, "Decision must be APPROVE or REJECT.", nil)
	}
	analysisID, err := parseUserUUID(raw.AnalysisID)
	if err != nil {
		return parsedReviewerDecision{}, participantAPIError(httpapi.CodeInvalidRequest, "A valid analysis_id is required.", err)
	}
	reason := strings.TrimSpace(raw.Reason)
	if decision == checkerDecisionReject && reason == "" {
		return parsedReviewerDecision{}, participantAPIError(httpapi.CodeValidationError, "Reason is required for a rejection.", nil)
	}
	if decision != checkerDecisionReject {
		reason = ""
	}

	evidenceIDs := make([]pgtype.UUID, 0, len(raw.EvidenceIDs))
	for _, value := range raw.EvidenceIDs {
		id, err := parseUserUUID(value)
		if err != nil {
			return parsedReviewerDecision{}, participantAPIError(httpapi.CodeInvalidRequest, "Each evidence_id must be a valid UUID.", err)
		}
		evidenceIDs = append(evidenceIDs, id)
	}
	return parsedReviewerDecision{
		analysisID:  analysisID,
		decision:    decision,
		comment:     strings.TrimSpace(raw.Comment),
		reason:      reason,
		evidenceIDs: evidenceIDs,
	}, nil
}

func recordCheckerApproval(
	ctx context.Context,
	store CheckerDecisionStore,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	decision *db.Decision,
	caseStatus *string,
) *httpapi.APIError {
	var apiErr *httpapi.APIError
	txErr := store.RunCheckerDecisionTx(ctx, func(ctx context.Context, q CheckerDecisionTxQueries) error {
		var stored db.Case
		var participants []db.CaseParticipant
		var decisions []db.Decision
		stored, participants, decisions, *decision, apiErr = persistReviewerDecision(ctx, q, caseID, actor, request, checkerDecisionConfig)
		if apiErr != nil {
			return apiErr
		}
		if apiErr = appendReviewerDecisionAudit(ctx, q, caseID, actor, request, checkerDecisionConfig, nil); apiErr != nil {
			return apiErr
		}

		*caseStatus = stored.Status
		if allRequiredCheckersApproved(request.analysisID, participants, append(decisions, *decision)) {
			next, err := workflow.Transition(workflow.State(stored.Status), workflow.EventAllCheckersApproved)
			if workflow.IsInvalidTransition(err) {
				apiErr = participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
				return apiErr
			}
			if err != nil {
				apiErr = participantInternalError(err)
				return apiErr
			}
			updated, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{ID: caseID, Status: string(next)})
			if err != nil {
				apiErr = participantInternalError(err)
				return apiErr
			}
			*caseStatus = updated.Status
		}
		return nil
	})
	if apiErr != nil {
		return apiErr
	}
	if txErr != nil {
		var wrapped *httpapi.APIError
		if errors.As(txErr, &wrapped) {
			return wrapped
		}
		return participantInternalError(txErr)
	}
	return nil
}

func recordCheckerRejection(
	ctx context.Context,
	orchestrator ReanalysisOrchestrator,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	decision *db.Decision,
	caseStatus *string,
) *httpapi.APIError {
	return recordReviewerRejection(ctx, orchestrator, caseID, actor, request, workflow.EventCheckerRejected, checkerDecisionConfig, decision, caseStatus)
}

func recordReviewerRejection(
	ctx context.Context,
	orchestrator ReanalysisOrchestrator,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	trigger workflow.Event,
	config reviewerDecisionConfig,
	decision *db.Decision,
	caseStatus *string,
) *httpapi.APIError {
	result, err := orchestrator.Run(ctx, reanalysis.Request{
		CaseID:        caseID,
		Trigger:       trigger,
		ActorID:       actor.ID,
		ActorRole:     config.actorRole,
		ModelName:     "",
		PromptVersion: "",
	}, func(ctx context.Context, tx db.DBTX, _ db.Case) error {
		q := reviewerDecisionQueries(tx)
		_, _, _, created, apiErr := persistReviewerDecision(ctx, q, caseID, actor, request, config)
		if apiErr != nil {
			return apiErr
		}
		feedback, apiErr := persistReviewerFeedbackEvidence(ctx, q, caseID, actor, request, config)
		if apiErr != nil {
			return apiErr
		}
		if apiErr = appendReviewerDecisionAudit(ctx, q, caseID, actor, request, config, &feedback.ID); apiErr != nil {
			return apiErr
		}
		*decision = created
		return nil
	})
	if err != nil {
		var apiErr *httpapi.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return participantAPIError(httpapi.CodeCaseNotFound, "", err)
		}
		if workflow.IsInvalidTransition(err) {
			return participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
		}
		return participantInternalError(err)
	}
	if result.Outcome != reanalysis.Queued && result.Outcome != reanalysis.LimitReached {
		return participantInternalError(errors.New("reanalysis returned an unknown outcome"))
	}
	*caseStatus = result.Case.Status
	return nil
}

func reviewerDecisionQueries(tx db.DBTX) DecisionTxQueries {
	// The direct form is useful for in-memory transaction fakes. Production
	// pgx transactions take the db.New path.
	if q, ok := tx.(DecisionTxQueries); ok {
		return q
	}
	return db.New(tx)
}

type reviewerDecisionConfig struct {
	actorRole                   string
	requiredState               workflow.State
	approvedEvent               string
	rejectedEvent               string
	feedbackTitle               string
	duplicateActorLabel         string
	requireApprovedCheckerRound bool
	unassignedErrorCode         httpapi.ErrorCode
}

var checkerDecisionConfig = reviewerDecisionConfig{
	actorRole:           caseRoleChecker,
	requiredState:       workflow.StateChecking,
	approvedEvent:       checkerEventApproved,
	rejectedEvent:       checkerEventRejected,
	feedbackTitle:       "Checker rejection feedback",
	duplicateActorLabel: "Checker",
	unassignedErrorCode: httpapi.CodeCaseNotFound,
}

func persistReviewerDecision(
	ctx context.Context,
	q DecisionTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	config reviewerDecisionConfig,
) (db.Case, []db.CaseParticipant, []db.Decision, db.Decision, *httpapi.APIError) {
	stored, err := q.GetCaseForUpdate(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
	}
	if stored.Status != string(config.requiredState) {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeInvalidStateTransition, "", nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
	}
	actorRole, allowed := participantActorRole(participants, actor)
	if !allowed {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(config.unassignedErrorCode, "", nil)
	}
	if actorRole != config.actorRole {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeForbidden, "", nil)
	}
	if !stored.CurrentAnalysisID.Valid || stored.CurrentAnalysisID != request.analysisID {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeStaleAnalysis, "", nil)
	}

	decisions, err := q.ListDecisionsByAnalysis(ctx, request.analysisID)
	if err != nil {
		return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
	}
	checkerRound := summarizeCheckerRound(request.analysisID, participants, decisions)
	if config.requireApprovedCheckerRound && (checkerRound.HasCheckerRejection || checkerRound.Approved != checkerRound.Required) {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeInvalidStateTransition, "All required checkers must approve the current analysis before signer review.", nil)
	}
	if config.actorRole == caseRoleChecker && request.decision == checkerDecisionApprove && checkerRound.HasCheckerRejection {
		return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeConflict, "Checker round has already been rejected.", nil)
	}
	for _, existing := range decisions {
		if existing.ActorID == actor.ID && existing.ActorRole == config.actorRole {
			return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeConflict, config.duplicateActorLabel+" has already decided this analysis.", nil)
		}
	}

	if len(request.evidenceIDs) > 0 {
		evidences, err := q.ListCaseEvidences(ctx, caseID)
		if err != nil {
			return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
		}
		available := make(map[pgtype.UUID]struct{}, len(evidences))
		for _, evidence := range evidences {
			available[evidence.ID] = struct{}{}
		}
		for _, evidenceID := range request.evidenceIDs {
			if _, ok := available[evidenceID]; !ok {
				return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeValidationError, "Evidence does not belong to this case.", nil)
			}
		}
	}

	decisionID, err := newUnitUUID()
	if err != nil {
		return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
	}
	created, err := q.CreateDecision(ctx, db.CreateDecisionParams{
		ID:         decisionID,
		CaseID:     caseID,
		AnalysisID: request.analysisID,
		ActorID:    actor.ID,
		ActorRole:  config.actorRole,
		Decision:   request.decision,
		Reason:     nullableCheckerText(request.reason),
		Comment:    nullableCheckerText(request.comment),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return db.Case{}, nil, nil, db.Decision{}, participantAPIError(httpapi.CodeConflict, config.duplicateActorLabel+" has already decided this analysis.", err)
		}
		return db.Case{}, nil, nil, db.Decision{}, participantInternalError(err)
	}

	return stored, participants, decisions, created, nil
}

func persistReviewerFeedbackEvidence(
	ctx context.Context,
	q DecisionTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	config reviewerDecisionConfig,
) (db.CaseEvidence, *httpapi.APIError) {
	evidenceID, err := newUnitUUID()
	if err != nil {
		return db.CaseEvidence{}, participantInternalError(err)
	}
	content := "Reason: " + request.reason
	if request.comment != "" {
		content += "\nComment: " + request.comment
	}
	if len(request.evidenceIDs) > 0 {
		citedEvidenceIDs := make([]string, 0, len(request.evidenceIDs))
		for _, id := range request.evidenceIDs {
			citedEvidenceIDs = append(citedEvidenceIDs, id.String())
		}
		content += "\nCited evidence: " + strings.Join(citedEvidenceIDs, ", ")
	}

	created, err := q.CreateEvidence(ctx, db.CreateEvidenceParams{
		ID:           evidenceID,
		CaseID:       caseID,
		SourceType:   config.actorRole,
		SourceUserID: actor.ID,
		EvidenceType: reviewerFeedbackEvidenceType,
		Title:        pgtype.Text{String: config.feedbackTitle, Valid: true},
		Content:      pgtype.Text{String: content, Valid: true},
		FilePath:     pgtype.Text{},
		MimeType:     pgtype.Text{},
	})
	if err != nil {
		return db.CaseEvidence{}, participantInternalError(err)
	}
	return created, nil
}

func appendReviewerDecisionAudit(
	ctx context.Context,
	q DecisionTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	config reviewerDecisionConfig,
	feedbackEvidenceID *pgtype.UUID,
) *httpapi.APIError {
	metadataEvidenceIDs := make([]string, 0, len(request.evidenceIDs))
	for _, id := range request.evidenceIDs {
		metadataEvidenceIDs = append(metadataEvidenceIDs, id.String())
	}
	var metadataFeedbackEvidenceID *string
	if feedbackEvidenceID != nil {
		value := feedbackEvidenceID.String()
		metadataFeedbackEvidenceID = &value
	}
	metadata, err := json.Marshal(struct {
		AnalysisID         string   `json:"analysis_id"`
		Decision           string   `json:"decision"`
		Reason             string   `json:"reason"`
		Comment            string   `json:"comment"`
		EvidenceIDs        []string `json:"evidence_ids"`
		FeedbackEvidenceID *string  `json:"feedback_evidence_id,omitempty"`
	}{
		AnalysisID:         request.analysisID.String(),
		Decision:           request.decision,
		Reason:             request.reason,
		Comment:            request.comment,
		EvidenceIDs:        metadataEvidenceIDs,
		FeedbackEvidenceID: metadataFeedbackEvidenceID,
	})
	if err != nil {
		return participantInternalError(err)
	}
	auditID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	eventType := config.approvedEvent
	if request.decision == checkerDecisionReject {
		eventType = config.rejectedEvent
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     caseID,
		EventType:  eventType,
		ActorID:    actor.ID,
		ActorRole:  nullableWorkflowActorRole(config.actorRole),
		AnalysisID: request.analysisID,
		Metadata:   metadata,
	}); err != nil {
		return participantInternalError(err)
	}
	return nil
}

func nullableCheckerText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
