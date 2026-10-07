package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	signerEventApproved = "SIGNER_APPROVED"
	signerEventRejected = "SIGNER_REJECTED"
)

var signerDecisionConfig = reviewerDecisionConfig{
	actorRole:                   caseRoleSigner,
	requiredState:               workflow.StateSigning,
	approvedEvent:               signerEventApproved,
	rejectedEvent:               signerEventRejected,
	feedbackTitle:               "Signer rejection feedback",
	duplicateActorLabel:         "Signer",
	requireApprovedCheckerRound: true,
	unassignedErrorCode:         httpapi.CodeForbidden,
}

type SignerDecider struct {
	Store      CheckerDecisionStore
	Reanalysis ReanalysisOrchestrator
}

// RecordSignerDecision records the assigned signer's decision for the current
// analysis. Approval advances to execution; rejection atomically persists
// feedback and delegates the re-analysis or escalation outcome.
func RecordSignerDecision(decider SignerDecider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if decider.Store == nil || isNilInterface(decider.Store) || decider.Reanalysis == nil || isNilInterface(decider.Reanalysis) {
			logging.With(r.Context()).Error("record signer decision: dependencies are not configured")
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
			apiErr = recordSignerApproval(r.Context(), decider.Store, caseID, actor, request, &decision, &caseStatus)
		} else {
			apiErr = recordReviewerRejection(r.Context(), decider.Reanalysis, caseID, actor, request, workflow.EventSignerRejected, signerDecisionConfig, &decision, &caseStatus)
		}
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("record signer decision", "case_id", caseID.String(), "error", apiErr)
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

func recordSignerApproval(
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
		stored, _, _, *decision, apiErr = persistReviewerDecision(ctx, q, caseID, actor, request, signerDecisionConfig)
		if apiErr != nil {
			return apiErr
		}
		if apiErr = appendReviewerDecisionAudit(ctx, q, caseID, actor, request, signerDecisionConfig, nil); apiErr != nil {
			return apiErr
		}

		next, err := workflow.Transition(workflow.State(stored.Status), workflow.EventSignerApproved)
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
