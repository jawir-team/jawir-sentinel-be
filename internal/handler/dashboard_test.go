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

type fakeDashboardStore struct {
	cases        []db.Case
	participants map[pgtype.UUID][]db.CaseParticipant
	decisions    map[pgtype.UUID][]db.Decision
	executions   map[db.ExistsExecutionForCaseAnalysisParams]bool

	listArg       db.ListCasesForUserParams
	listCalls     int
	executionArgs []db.ExistsExecutionForCaseAnalysisParams
}

var _ handler.DashboardStore = (*fakeDashboardStore)(nil)

func (f *fakeDashboardStore) ListCasesForUser(_ context.Context, arg db.ListCasesForUserParams) ([]db.Case, error) {
	f.listCalls++
	f.listArg = arg
	return f.cases, nil
}

func (f *fakeDashboardStore) ListCaseParticipants(_ context.Context, caseID pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants[caseID], nil
}

func (f *fakeDashboardStore) ListDecisionsByAnalysis(_ context.Context, analysisID pgtype.UUID) ([]db.Decision, error) {
	return f.decisions[analysisID], nil
}

func (f *fakeDashboardStore) ExistsExecutionForCaseAnalysis(_ context.Context, arg db.ExistsExecutionForCaseAnalysisParams) (bool, error) {
	f.executionArgs = append(f.executionArgs, arg)
	return f.executions[arg], nil
}

func TestDashboardSummaryNoAssignedCases(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := &fakeDashboardStore{}

	response := serveDashboardSummary(store, actor)

	assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(0, 0, 0, 0, zeroDashboardStatusCounts()))
	if store.listCalls != 1 {
		t.Errorf("ListCasesForUser calls = %d, want 1", store.listCalls)
	}
	if store.listArg.IsAdmin {
		t.Error("IsAdmin = true, want false")
	}
	if store.listArg.UserID != actor.ID {
		t.Errorf("UserID = %v, want %v", store.listArg.UserID, actor.ID)
	}
}

func TestDashboardSummaryMixedStatusCounts(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	statuses := []string{
		"DRAFT",
		"SUBMITTED",
		"AI_ANALYSIS",
		"CHECKING",
		"CHECKING",
		"SIGNING",
		"EXECUTION",
		"ESCALATION_REQUIRED",
		"DONE",
		"DONE",
		"CLOSED",
	}
	store := &fakeDashboardStore{}
	for i, status := range statuses {
		store.cases = append(store.cases, dashboardCase(byte(i+1), status))
	}
	counts := zeroDashboardStatusCounts()
	counts["DRAFT"] = float64(1)
	counts["SUBMITTED"] = float64(1)
	counts["AI_ANALYSIS"] = float64(1)
	counts["CHECKING"] = float64(2)
	counts["SIGNING"] = float64(1)
	counts["EXECUTION"] = float64(1)
	counts["ESCALATION_REQUIRED"] = float64(1)
	counts["DONE"] = float64(2)
	counts["CLOSED"] = float64(1)

	response := serveDashboardSummary(store, actor)

	assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(len(statuses), 0, 0, 0, counts))
}

func TestDashboardSummaryNeedMyReview(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	stored := dashboardCase(1, "CHECKING")
	checker := db.CaseParticipant{
		CaseID:   stored.ID,
		UserID:   actor.ID,
		Role:     "CHECKER",
		Required: true,
		Status:   "ACTIVE",
	}
	approval := db.Decision{
		CaseID:     stored.ID,
		AnalysisID: stored.CurrentAnalysisID,
		ActorID:    actor.ID,
		ActorRole:  "CHECKER",
		Decision:   "APPROVE",
	}

	tests := []struct {
		name         string
		participants []db.CaseParticipant
		decisions    []db.Decision
		want         int
	}{
		{name: "active required checker without approval", participants: []db.CaseParticipant{checker}, want: 1},
		{name: "checker already approved", participants: []db.CaseParticipant{checker}, decisions: []db.Decision{approval}, want: 0},
		{name: "actor is not checker", participants: []db.CaseParticipant{{CaseID: stored.ID, UserID: actor.ID, Role: "SIGNER", Status: "ACTIVE"}}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDashboardStore{
				cases:        []db.Case{stored},
				participants: map[pgtype.UUID][]db.CaseParticipant{stored.ID: tt.participants},
				decisions:    map[pgtype.UUID][]db.Decision{stored.CurrentAnalysisID: tt.decisions},
			}

			response := serveDashboardSummary(store, actor)

			counts := zeroDashboardStatusCounts()
			counts["CHECKING"] = float64(1)
			assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(1, tt.want, 0, 0, counts))
		})
	}
}

func TestDashboardSummaryNeedMySignature(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	stored := dashboardCase(2, "SIGNING")
	signer := db.CaseParticipant{CaseID: stored.ID, UserID: actor.ID, Role: "SIGNER", Status: "ACTIVE"}
	approval := db.Decision{
		CaseID:     stored.ID,
		AnalysisID: stored.CurrentAnalysisID,
		ActorID:    actor.ID,
		ActorRole:  "SIGNER",
		Decision:   "APPROVE",
	}

	for _, tt := range []struct {
		name      string
		decisions []db.Decision
		want      int
	}{
		{name: "awaiting signature", want: 1},
		{name: "already approved", decisions: []db.Decision{approval}, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDashboardStore{
				cases:        []db.Case{stored},
				participants: map[pgtype.UUID][]db.CaseParticipant{stored.ID: []db.CaseParticipant{signer}},
				decisions:    map[pgtype.UUID][]db.Decision{stored.CurrentAnalysisID: tt.decisions},
			}

			response := serveDashboardSummary(store, actor)

			counts := zeroDashboardStatusCounts()
			counts["SIGNING"] = float64(1)
			assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(1, 0, tt.want, 0, counts))
		})
	}
}

func TestDashboardSummaryNeedMyExecution(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	stored := dashboardCase(3, "EXECUTION")
	executer := db.CaseParticipant{CaseID: stored.ID, UserID: actor.ID, Role: "EXECUTER", Status: "ACTIVE"}
	executionArg := db.ExistsExecutionForCaseAnalysisParams{CaseID: stored.ID, AnalysisID: stored.CurrentAnalysisID}

	for _, tt := range []struct {
		name   string
		exists bool
		want   int
	}{
		{name: "awaiting execution", want: 1},
		{name: "execution exists", exists: true, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDashboardStore{
				cases:        []db.Case{stored},
				participants: map[pgtype.UUID][]db.CaseParticipant{stored.ID: []db.CaseParticipant{executer}},
				executions:   map[db.ExistsExecutionForCaseAnalysisParams]bool{executionArg: tt.exists},
			}

			response := serveDashboardSummary(store, actor)

			counts := zeroDashboardStatusCounts()
			counts["EXECUTION"] = float64(1)
			assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(1, 0, 0, tt.want, counts))
			if len(store.executionArgs) != 1 || store.executionArgs[0] != executionArg {
				t.Errorf("ExistsExecutionForCaseAnalysis args = %v, want [%v]", store.executionArgs, executionArg)
			}
		})
	}
}

func TestDashboardSummaryAdminListsAllCases(t *testing.T) {
	admin := caseTestUser(auth.SystemRoleAdmin)
	store := &fakeDashboardStore{}

	response := serveDashboardSummary(store, admin)

	assertJSONResponse(t, response, http.StatusOK, dashboardSummaryJSON(0, 0, 0, 0, zeroDashboardStatusCounts()))
	if !store.listArg.IsAdmin {
		t.Error("IsAdmin = false, want true for admin")
	}
	if store.listArg.UserID != admin.ID {
		t.Errorf("UserID = %v, want %v", store.listArg.UserID, admin.ID)
	}
}

func TestDashboardSummaryResponseShape(t *testing.T) {
	response := serveDashboardSummary(&fakeDashboardStore{}, caseTestUser(auth.SystemRoleUser))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertExactDashboardKeys(t, body, "data")
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want object", body["data"])
	}
	assertExactDashboardKeys(t, data, "my_cases", "need_my_review", "need_my_signature", "need_my_execution", "status_counts")
	statusCounts, ok := data["status_counts"].(map[string]any)
	if !ok {
		t.Fatalf("status_counts = %#v, want object", data["status_counts"])
	}
	assertExactDashboardKeys(t, statusCounts,
		"DRAFT",
		"SUBMITTED",
		"AI_ANALYSIS",
		"CHECKING",
		"SIGNING",
		"EXECUTION",
		"ESCALATION_REQUIRED",
		"DONE",
		"CLOSED",
	)
}

func dashboardCase(id byte, status string) db.Case {
	return db.Case{
		ID:                handlerTestUUID(id),
		Status:            status,
		CurrentAnalysisID: handlerTestUUID(id + 100),
	}
}

func serveDashboardSummary(store handler.DashboardStore, actor auth.User) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	request = request.WithContext(auth.WithUser(request.Context(), actor))
	response := httptest.NewRecorder()
	handler.GetDashboardSummary(store).ServeHTTP(response, request)
	return response
}

func zeroDashboardStatusCounts() map[string]any {
	return map[string]any{
		"DRAFT":               float64(0),
		"SUBMITTED":           float64(0),
		"AI_ANALYSIS":         float64(0),
		"CHECKING":            float64(0),
		"SIGNING":             float64(0),
		"EXECUTION":           float64(0),
		"ESCALATION_REQUIRED": float64(0),
		"DONE":                float64(0),
		"CLOSED":              float64(0),
	}
}

func dashboardSummaryJSON(myCases, review, signature, execution int, statusCounts map[string]any) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"my_cases":          float64(myCases),
			"need_my_review":    float64(review),
			"need_my_signature": float64(signature),
			"need_my_execution": float64(execution),
			"status_counts":     statusCounts,
		},
	}
}

func assertExactDashboardKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("object keys = %v, want exactly %v", dashboardKeys(object), want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Errorf("object is missing key %q; keys=%v", key, dashboardKeys(object))
		}
	}
}

func dashboardKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}
