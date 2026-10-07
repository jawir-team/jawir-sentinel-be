package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
)

type fakeCheckerStatusStore struct {
	caseResult   db.Case
	participants []db.CaseParticipant
	decisions    []db.Decision
	users        map[pgtype.UUID]db.User

	decisionCalls int
	userCalls     int
}

var _ handler.CheckerStatusStore = (*fakeCheckerStatusStore)(nil)

func (f *fakeCheckerStatusStore) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, nil
}

func (f *fakeCheckerStatusStore) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, nil
}

func (f *fakeCheckerStatusStore) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) {
	f.decisionCalls++
	return f.decisions, nil
}

func (f *fakeCheckerStatusStore) GetUser(_ context.Context, id pgtype.UUID) (db.User, error) {
	f.userCalls++
	return f.users[id], nil
}

func TestGetCheckerStatusResponseUsesOnlyCurrentAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4)
	analysisID := handlerTestUUID(5)
	oldAnalysisID := handlerTestUUID(7)
	approvedID := handlerTestUUID(8)
	pendingID := handlerTestUUID(9)
	optionalID := handlerTestUUID(10)
	store := &fakeCheckerStatusStore{
		caseResult: db.Case{ID: caseID, CurrentAnalysisID: analysisID},
		participants: []db.CaseParticipant{
			{CaseID: caseID, UserID: actor.ID, Role: "MAKER", Required: true, Status: "ACTIVE"},
			{CaseID: caseID, UserID: approvedID, Role: "CHECKER", Required: true, Status: "ACTIVE"},
			{CaseID: caseID, UserID: pendingID, Role: "CHECKER", Required: true, Status: "ACTIVE"},
			{CaseID: caseID, UserID: optionalID, Role: "CHECKER", Required: false, Status: "ACTIVE"},
			{CaseID: caseID, UserID: handlerTestUUID(11), Role: "CHECKER", Required: true, Status: "INACTIVE"},
		},
		decisions: []db.Decision{
			{AnalysisID: analysisID, ActorID: approvedID, ActorRole: "CHECKER", Decision: "APPROVE"},
			{AnalysisID: oldAnalysisID, ActorID: pendingID, ActorRole: "CHECKER", Decision: "APPROVE"},
			{AnalysisID: analysisID, ActorID: optionalID, ActorRole: "CHECKER", Decision: "REJECT"},
		},
		users: map[pgtype.UUID]db.User{
			approvedID: {ID: approvedID, Name: "Risk User"},
			pendingID:  {ID: pendingID, Name: "Dev User"},
			optionalID: {ID: optionalID, Name: "Optional User"},
		},
	}

	request := caseRequestWithID(http.MethodGet, "/api/v1/cases/"+caseID.String()+"/checker-status", caseID.String(), "", &actor)
	response := httptest.NewRecorder()
	handler.GetCheckerStatus(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			AnalysisID string `json:"analysis_id"`
			Required   int    `json:"required"`
			Approved   int    `json:"approved"`
			Rejected   int    `json:"rejected"`
			Pending    int    `json:"pending"`
			Checkers   []struct {
				UserID string `json:"user_id"`
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checkers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.AnalysisID != analysisID.String() || body.Data.Required != 2 || body.Data.Approved != 1 || body.Data.Rejected != 0 || body.Data.Pending != 1 {
		t.Errorf("data counts = %+v, want analysis=%s required=2 approved=1 rejected=0 pending=1", body.Data, analysisID.String())
	}
	wantCheckers := []struct {
		id, name, status string
	}{
		{approvedID.String(), "Risk User", "APPROVED"},
		{pendingID.String(), "Dev User", "PENDING"},
		{optionalID.String(), "Optional User", "REJECTED"},
	}
	if len(body.Data.Checkers) != len(wantCheckers) {
		t.Fatalf("checkers = %+v, want %d entries", body.Data.Checkers, len(wantCheckers))
	}
	for i, want := range wantCheckers {
		got := body.Data.Checkers[i]
		if got.UserID != want.id || got.Name != want.name || got.Status != want.status {
			t.Errorf("checkers[%d] = %+v, want id=%s name=%q status=%s", i, got, want.id, want.name, want.status)
		}
	}
	if store.decisionCalls != 1 || store.userCalls != 3 {
		t.Errorf("lookup calls = decisions %d users %d, want 1 and 3", store.decisionCalls, store.userCalls)
	}
}

func TestGetCheckerStatusWithoutCurrentAnalysisIsEmpty(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4)
	store := &fakeCheckerStatusStore{
		caseResult: db.Case{ID: caseID},
		participants: []db.CaseParticipant{
			{CaseID: caseID, UserID: actor.ID, Role: "MAKER", Status: "ACTIVE"},
		},
	}

	request := caseRequestWithID(http.MethodGet, "/api/v1/cases/"+caseID.String()+"/checker-status", caseID.String(), "", &actor)
	response := httptest.NewRecorder()
	handler.GetCheckerStatus(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			AnalysisID *string `json:"analysis_id"`
			Required   int     `json:"required"`
			Approved   int     `json:"approved"`
			Rejected   int     `json:"rejected"`
			Pending    int     `json:"pending"`
			Checkers   []any   `json:"checkers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.AnalysisID != nil || body.Data.Required != 0 || body.Data.Approved != 0 || body.Data.Rejected != 0 || body.Data.Pending != 0 || len(body.Data.Checkers) != 0 {
		t.Errorf("data = %+v, want null analysis and empty zero-valued round", body.Data)
	}
	if store.decisionCalls != 0 || store.userCalls != 0 {
		t.Errorf("lookup calls = decisions %d users %d, want zero", store.decisionCalls, store.userCalls)
	}
}
