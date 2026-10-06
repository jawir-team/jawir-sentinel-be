package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

func TestValidateCaseSoD(t *testing.T) {
	userID := handlerTestUUID(1)
	violations := []struct {
		name  string
		roles [2]string
	}{
		{name: "Maker-Checker", roles: [2]string{"MAKER", "CHECKER"}},
		{name: "Maker-Signer", roles: [2]string{"MAKER", "SIGNER"}},
		{name: "Maker-Executer", roles: [2]string{"MAKER", "EXECUTER"}},
		{name: "Checker-Signer", roles: [2]string{"CHECKER", "SIGNER"}},
		{name: "Checker-Executer", roles: [2]string{"CHECKER", "EXECUTER"}},
		{name: "Signer-Executer", roles: [2]string{"SIGNER", "EXECUTER"}},
	}
	for _, tt := range violations {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := handler.ValidateCaseSoD([]db.CaseParticipant{
				{UserID: userID, Role: tt.roles[0], Status: "ACTIVE"},
				{UserID: userID, Role: tt.roles[1], Status: "ACTIVE"},
			})
			if apiErr == nil {
				t.Fatal("ValidateCaseSoD() error = nil, want segregation-of-duties violation")
			}

			response := httptest.NewRecorder()
			httpapi.WriteError(response, apiErr)
			assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeSegregationOfDutiesViolation)
		})
	}

	validCases := []struct {
		name         string
		participants []db.CaseParticipant
	}{
		{
			name: "distinct users per role",
			participants: []db.CaseParticipant{
				{UserID: handlerTestUUID(1), Role: "MAKER", Status: "ACTIVE"},
				{UserID: handlerTestUUID(2), Role: "CHECKER", Status: "ACTIVE"},
				{UserID: handlerTestUUID(3), Role: "SIGNER", Status: "ACTIVE"},
				{UserID: handlerTestUUID(4), Role: "EXECUTER", Status: "ACTIVE"},
			},
		},
		{
			name: "two different users as checker",
			participants: []db.CaseParticipant{
				{UserID: handlerTestUUID(1), Role: "CHECKER", Status: "ACTIVE"},
				{UserID: handlerTestUUID(2), Role: "CHECKER", Status: "ACTIVE"},
			},
		},
		{
			name: "inactive role is ignored",
			participants: []db.CaseParticipant{
				{UserID: userID, Role: "CHECKER", Status: "ACTIVE"},
				{UserID: userID, Role: "SIGNER", Status: "INACTIVE"},
			},
		},
		{name: "empty list", participants: []db.CaseParticipant{}},
		{name: "nil list", participants: nil},
	}
	for _, tt := range validCases {
		t.Run(tt.name, func(t *testing.T) {
			if apiErr := handler.ValidateCaseSoD(tt.participants); apiErr != nil {
				t.Fatalf("ValidateCaseSoD() error = %v, want nil", apiErr)
			}
		})
	}
}

func TestAssignCaseParticipantRejectsLatentSoDViolation(t *testing.T) {
	actor := auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleUser}
	caseID := handlerTestUUID(4)
	violatingUserID := handlerTestUUID(7)
	queries := participantTestQueries(actor)
	queries.participants = append(queries.participants,
		db.CaseParticipant{
			ID: handlerTestUUID(10), CaseID: caseID, UserID: violatingUserID, Role: "CHECKER", Status: "ACTIVE",
		},
		db.CaseParticipant{
			ID: handlerTestUUID(11), CaseID: caseID, UserID: violatingUserID, Role: "SIGNER", Status: "ACTIVE",
		},
	)

	response := serveAssignParticipant(
		actor,
		`{"user_id":"00000000-0000-0000-0000-000000000008","role":"EXECUTER"}`,
		queries,
	)

	assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeSegregationOfDutiesViolation)
	if queries.auditCalls != 0 {
		t.Errorf("AppendCaseAuditEvent calls = %d, want 0", queries.auditCalls)
	}
}
