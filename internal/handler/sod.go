package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

// ValidateCaseSoD verifies that each user holds at most one active workflow
// role in a case.
func ValidateCaseSoD(participants []db.CaseParticipant) *httpapi.APIError {
	rolesByUser := make(map[pgtype.UUID]map[string]struct{})
	for _, participant := range participants {
		if participant.Status != participantStatusActive || !isCaseWorkflowRole(participant.Role) {
			continue
		}

		roles := rolesByUser[participant.UserID]
		if roles == nil {
			roles = make(map[string]struct{})
			rolesByUser[participant.UserID] = roles
		}
		roles[participant.Role] = struct{}{}
		if len(roles) >= 2 {
			return httpapi.NewError(
				httpapi.CodeSegregationOfDutiesViolation,
				"A user cannot hold more than one workflow role in a case.",
				nil,
			)
		}
	}
	return nil
}

func isCaseWorkflowRole(role string) bool {
	switch role {
	case caseRoleMaker, caseRoleChecker, caseRoleSigner, caseRoleExecuter:
		return true
	default:
		return false
	}
}
