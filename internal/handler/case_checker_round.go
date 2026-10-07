package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

const (
	checkerStatusApproved = "APPROVED"
	checkerStatusRejected = "REJECTED"
	checkerStatusPending  = "PENDING"
)

// checkerRoundSummary is the single source of truth for checker completion
// and the checker-status endpoint. Only decisions for the supplied analysis
// participate in a round.
type checkerRoundSummary struct {
	Required            int
	Approved            int
	Rejected            int
	Pending             int
	Statuses            map[pgtype.UUID]string
	HasCheckerRejection bool
}

func summarizeCheckerRound(
	analysisID pgtype.UUID,
	participants []db.CaseParticipant,
	decisions []db.Decision,
) checkerRoundSummary {
	statuses := make(map[pgtype.UUID]string, len(decisions))
	hasCheckerRejection := false
	for _, decision := range decisions {
		if !analysisID.Valid || decision.AnalysisID != analysisID || decision.ActorRole != caseRoleChecker {
			continue
		}
		switch decision.Decision {
		case checkerDecisionReject:
			// A rejection wins defensively if inconsistent duplicate input is
			// ever supplied, even though the database uniqueness constraint
			// permits only one checker decision per analysis.
			statuses[decision.ActorID] = checkerStatusRejected
			hasCheckerRejection = true
		case checkerDecisionApprove:
			if statuses[decision.ActorID] != checkerStatusRejected {
				statuses[decision.ActorID] = checkerStatusApproved
			}
		}
	}

	summary := checkerRoundSummary{
		Statuses:            statuses,
		HasCheckerRejection: hasCheckerRejection,
	}
	for _, participant := range participants {
		if participant.Status != participantStatusActive || participant.Role != caseRoleChecker || !participant.Required {
			continue
		}
		summary.Required++
		switch statuses[participant.UserID] {
		case checkerStatusApproved:
			summary.Approved++
		case checkerStatusRejected:
			summary.Rejected++
		}
	}
	summary.Pending = summary.Required - summary.Approved - summary.Rejected
	return summary
}

func allRequiredCheckersApproved(
	analysisID pgtype.UUID,
	participants []db.CaseParticipant,
	decisions []db.Decision,
) bool {
	summary := summarizeCheckerRound(analysisID, participants, decisions)
	return !summary.HasCheckerRejection && summary.Approved == summary.Required
}
