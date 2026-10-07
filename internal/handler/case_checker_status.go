package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

// CheckerStatusStore is the read-only database boundary for checker status.
// *db.Queries and *TxQueries both satisfy it.
type CheckerStatusStore interface {
	GetCase(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error)
	GetUser(context.Context, pgtype.UUID) (db.User, error)
}

var _ CheckerStatusStore = (*db.Queries)(nil)

type checkerStatusResponse struct {
	AnalysisID *pgtype.UUID          `json:"analysis_id"`
	Required   int                   `json:"required"`
	Approved   int                   `json:"approved"`
	Rejected   int                   `json:"rejected"`
	Pending    int                   `json:"pending"`
	Checkers   []checkerStatusMember `json:"checkers"`
}

type checkerStatusMember struct {
	UserID pgtype.UUID `json:"user_id"`
	Name   string      `json:"name"`
	Status string      `json:"status"`
}

// GetCheckerStatus reports the current analysis's checker round. Access is
// deliberately restricted to active case participants.
func GetCheckerStatus(store CheckerStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("get checker status: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}

		response, apiErr := getCheckerStatus(r.Context(), store, caseID, actor)
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("get checker status", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

func getCheckerStatus(
	ctx context.Context,
	store CheckerStatusStore,
	caseID pgtype.UUID,
	actor auth.User,
) (checkerStatusResponse, *httpapi.APIError) {
	stored, err := store.GetCase(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return checkerStatusResponse{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return checkerStatusResponse{}, participantInternalError(err)
	}

	participants, err := store.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return checkerStatusResponse{}, participantInternalError(err)
	}
	if !isActiveCaseParticipant(participants, actor.ID) {
		// Match case read endpoints by concealing whether an inaccessible case
		// exists, while intentionally not granting a global-admin exception.
		return checkerStatusResponse{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}

	response := checkerStatusResponse{Checkers: make([]checkerStatusMember, 0)}
	if !stored.CurrentAnalysisID.Valid {
		return response, nil
	}
	analysisID := stored.CurrentAnalysisID
	response.AnalysisID = &analysisID

	decisions, err := store.ListDecisionsByAnalysis(ctx, analysisID)
	if err != nil {
		return checkerStatusResponse{}, participantInternalError(err)
	}
	summary := summarizeCheckerRound(analysisID, participants, decisions)
	response.Required = summary.Required
	response.Approved = summary.Approved
	response.Rejected = summary.Rejected
	response.Pending = summary.Pending

	for _, participant := range participants {
		if participant.Status != participantStatusActive || participant.Role != caseRoleChecker {
			continue
		}
		user, err := store.GetUser(ctx, participant.UserID)
		if err != nil {
			return checkerStatusResponse{}, participantInternalError(err)
		}
		status := summary.Statuses[participant.UserID]
		if status == "" {
			status = checkerStatusPending
		}
		response.Checkers = append(response.Checkers, checkerStatusMember{
			UserID: participant.UserID,
			Name:   user.Name,
			Status: status,
		})
	}
	return response, nil
}

func isActiveCaseParticipant(participants []db.CaseParticipant, userID pgtype.UUID) bool {
	for _, participant := range participants {
		if participant.Status == participantStatusActive && participant.UserID == userID {
			return true
		}
	}
	return false
}
