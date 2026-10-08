package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

// DashboardStore is the read-only database boundary for the dashboard summary.
type DashboardStore interface {
	ListCasesForUser(context.Context, db.ListCasesForUserParams) ([]db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error)
	ExistsExecutionForCaseAnalysis(context.Context, db.ExistsExecutionForCaseAnalysisParams) (bool, error)
}

var _ DashboardStore = (*db.Queries)(nil)

var dashboardWorkflowStates = [...]workflow.State{
	workflow.StateDraft,
	workflow.StateSubmitted,
	workflow.StateAIAnalysis,
	workflow.StateChecking,
	workflow.StateSigning,
	workflow.StateExecution,
	workflow.StateEscalationRequired,
	workflow.StateDone,
	workflow.StateClosed,
}

type dashboardSummary struct {
	MyCases         int            `json:"my_cases"`
	NeedMyReview    int            `json:"need_my_review"`
	NeedMySignature int            `json:"need_my_signature"`
	NeedMyExecution int            `json:"need_my_execution"`
	StatusCounts    map[string]int `json:"status_counts"`
}

// GetDashboardSummary reports counts over cases visible to the authenticated
// actor. It does not mutate workflow or database state.
func GetDashboardSummary(store DashboardStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("get dashboard summary: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		summary, err := buildDashboardSummary(r.Context(), store, actor)
		if err != nil {
			logging.With(r.Context()).Error("get dashboard summary", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: summary})
	}
}

func buildDashboardSummary(ctx context.Context, store DashboardStore, actor auth.User) (dashboardSummary, error) {
	statusCounts := make(map[string]int, len(dashboardWorkflowStates))
	for _, state := range dashboardWorkflowStates {
		statusCounts[string(state)] = 0
	}

	storedCases, err := store.ListCasesForUser(ctx, db.ListCasesForUserParams{
		IsAdmin: actor.SystemRole == auth.SystemRoleAdmin,
		UserID:  actor.ID,
	})
	if err != nil {
		return dashboardSummary{}, fmt.Errorf("list visible cases: %w", err)
	}

	summary := dashboardSummary{
		MyCases:      len(storedCases),
		StatusCounts: statusCounts,
	}
	for _, stored := range storedCases {
		if _, known := summary.StatusCounts[stored.Status]; known {
			summary.StatusCounts[stored.Status]++
		}

		// MVP: actionable counts use per-case participant and decision/execution
		// lookups. These can be replaced by aggregate queries if volume requires it.
		switch workflow.State(stored.Status) {
		case workflow.StateChecking:
			pending, err := actorNeedsApproval(ctx, store, stored, actor.ID, caseRoleChecker)
			if err != nil {
				return dashboardSummary{}, err
			}
			if pending {
				summary.NeedMyReview++
			}
		case workflow.StateSigning:
			pending, err := actorNeedsApproval(ctx, store, stored, actor.ID, caseRoleSigner)
			if err != nil {
				return dashboardSummary{}, err
			}
			if pending {
				summary.NeedMySignature++
			}
		case workflow.StateExecution:
			pending, err := actorNeedsExecution(ctx, store, stored, actor.ID)
			if err != nil {
				return dashboardSummary{}, err
			}
			if pending {
				summary.NeedMyExecution++
			}
		}
	}

	return summary, nil
}

func actorNeedsApproval(
	ctx context.Context,
	store DashboardStore,
	stored db.Case,
	actorID pgtype.UUID,
	role string,
) (bool, error) {
	if !stored.CurrentAnalysisID.Valid {
		return false, nil
	}
	participants, err := store.ListCaseParticipants(ctx, stored.ID)
	if err != nil {
		return false, fmt.Errorf("list participants for case %s: %w", stored.ID.String(), err)
	}
	if !hasActiveCaseRole(participants, actorID, role) {
		return false, nil
	}

	decisions, err := store.ListDecisionsByAnalysis(ctx, stored.CurrentAnalysisID)
	if err != nil {
		return false, fmt.Errorf("list decisions for analysis %s: %w", stored.CurrentAnalysisID.String(), err)
	}
	for _, decision := range decisions {
		if decision.AnalysisID == stored.CurrentAnalysisID &&
			decision.ActorID == actorID &&
			decision.ActorRole == role &&
			decision.Decision == checkerDecisionApprove {
			return false, nil
		}
	}
	return true, nil
}

func actorNeedsExecution(
	ctx context.Context,
	store DashboardStore,
	stored db.Case,
	actorID pgtype.UUID,
) (bool, error) {
	if !stored.CurrentAnalysisID.Valid {
		return false, nil
	}
	participants, err := store.ListCaseParticipants(ctx, stored.ID)
	if err != nil {
		return false, fmt.Errorf("list participants for case %s: %w", stored.ID.String(), err)
	}
	if !hasActiveCaseRole(participants, actorID, caseRoleExecuter) {
		return false, nil
	}

	exists, err := store.ExistsExecutionForCaseAnalysis(ctx, db.ExistsExecutionForCaseAnalysisParams{
		CaseID:     stored.ID,
		AnalysisID: stored.CurrentAnalysisID,
	})
	if err != nil {
		return false, fmt.Errorf("check execution for case %s analysis %s: %w", stored.ID.String(), stored.CurrentAnalysisID.String(), err)
	}
	return !exists, nil
}

func hasActiveCaseRole(participants []db.CaseParticipant, actorID pgtype.UUID, role string) bool {
	for _, participant := range participants {
		if participant.UserID == actorID && participant.Role == role && participant.Status == participantStatusActive {
			return true
		}
	}
	return false
}
