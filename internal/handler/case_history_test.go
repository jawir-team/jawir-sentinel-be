package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeHistoryStore struct {
	caseResult db.Case
	caseErr    error

	participants     []db.CaseParticipant
	participantsErr  error
	participantCalls int

	events       []db.AuditEvent
	eventsErr    error
	eventsCalls  int
	eventsCaseID pgtype.UUID

	analyses      map[pgtype.UUID]db.AiAnalysis
	analysisErr   error
	analysisCalls int
}

var _ handler.HistoryStore = (*fakeHistoryStore)(nil)

func (f *fakeHistoryStore) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, f.caseErr
}

func (f *fakeHistoryStore) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	f.participantCalls++
	return f.participants, f.participantsErr
}

func (f *fakeHistoryStore) ListCaseAuditEvents(_ context.Context, caseID pgtype.UUID) ([]db.AuditEvent, error) {
	f.eventsCalls++
	f.eventsCaseID = caseID
	return f.events, f.eventsErr
}

func (f *fakeHistoryStore) GetAnalysis(_ context.Context, analysisID pgtype.UUID) (db.AiAnalysis, error) {
	f.analysisCalls++
	if f.analysisErr != nil {
		return db.AiAnalysis{}, f.analysisErr
	}
	return f.analyses[analysisID], nil
}

type historyTestItem struct {
	EventID         string         `json:"event_id"`
	EventType       string         `json:"event_type"`
	CreatedAt       string         `json:"created_at"`
	ActorID         *string        `json:"actor_id"`
	ActorRole       *string        `json:"actor_role"`
	AnalysisID      *string        `json:"analysis_id"`
	AnalysisVersion *int32         `json:"analysis_version"`
	Metadata        map[string]any `json:"metadata"`
}

func historyStoreFor(actor auth.User, events ...db.AuditEvent) *fakeHistoryStore {
	caseID := handlerTestUUID(4)
	return &fakeHistoryStore{
		caseResult: db.Case{ID: caseID},
		participants: []db.CaseParticipant{{
			ID: handlerTestUUID(30), CaseID: caseID, UserID: actor.ID, Role: "MAKER", Status: "ACTIVE",
		}},
		events:   events,
		analyses: make(map[pgtype.UUID]db.AiAnalysis),
	}
}

func getHistory(t *testing.T, store handler.HistoryStore, actor auth.User) (*httptest.ResponseRecorder, []historyTestItem) {
	t.Helper()
	caseID := handlerTestUUID(4)
	request := caseRequestWithID(http.MethodGet, "/api/v1/cases/"+caseID.String()+"/history", caseID.String(), "", &actor)
	response := httptest.NewRecorder()
	handler.GetCaseHistory(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		return response, nil
	}
	var envelope struct {
		Data []historyTestItem `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode history response: %v; body=%s", err, response.Body.String())
	}
	return response, envelope.Data
}

func TestGetCaseHistoryFullHistory(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4)
	analysisID := handlerTestUUID(6)
	firstAt := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Minute)
	store := historyStoreFor(actor,
		db.AuditEvent{
			ID: handlerTestUUID(11), ScopeType: "CASE", CaseID: caseID, EventType: "CASE_CREATED",
			ActorID: actor.ID, ActorRole: pgtype.Text{String: "MAKER", Valid: true},
			Metadata: []byte(`{"status":"DRAFT"}`), CreatedAt: firstAt,
		},
		db.AuditEvent{
			ID: handlerTestUUID(12), ScopeType: "CASE", CaseID: caseID, EventType: "AI_ANALYSIS_COMPLETED",
			AnalysisID: analysisID, Metadata: []byte(`{"version":2}`), CreatedAt: secondAt,
		},
	)
	store.analyses[analysisID] = db.AiAnalysis{ID: analysisID, CaseID: caseID, Version: 2}

	response, items := getHistory(t, store, actor)
	if response.Code != http.StatusOK {
		t.Fatalf("GET history = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if len(items) != 2 {
		t.Fatalf("history length = %d, want 2", len(items))
	}
	if items[0].EventID != handlerTestUUID(11).String() || items[0].EventType != "CASE_CREATED" || items[0].CreatedAt != "2026-10-01T09:30:00Z" {
		t.Errorf("first event = %+v, want stored CASE_CREATED event", items[0])
	}
	if items[0].ActorID == nil || *items[0].ActorID != actor.ID.String() || items[0].ActorRole == nil || *items[0].ActorRole != "MAKER" {
		t.Errorf("first event actor = %v/%v, want %s/MAKER", items[0].ActorID, items[0].ActorRole, actor.ID.String())
	}
	if items[0].AnalysisID != nil || items[0].AnalysisVersion != nil {
		t.Errorf("first event analysis = %v/%v, want null/null", items[0].AnalysisID, items[0].AnalysisVersion)
	}
	if items[0].Metadata["status"] != "DRAFT" {
		t.Errorf("first event metadata = %v, want status DRAFT", items[0].Metadata)
	}
	if items[1].EventID != handlerTestUUID(12).String() || items[1].EventType != "AI_ANALYSIS_COMPLETED" || items[1].CreatedAt != "2026-10-01T09:31:00Z" {
		t.Errorf("second event = %+v, want stored AI_ANALYSIS_COMPLETED event", items[1])
	}
	if items[1].AnalysisID == nil || *items[1].AnalysisID != analysisID.String() || items[1].AnalysisVersion == nil || *items[1].AnalysisVersion != 2 {
		t.Errorf("second event analysis = %v/%v, want %s/2", items[1].AnalysisID, items[1].AnalysisVersion, analysisID.String())
	}
	if items[1].ActorID != nil || items[1].ActorRole != nil {
		t.Errorf("second event actor = %v/%v, want null/null", items[1].ActorID, items[1].ActorRole)
	}
	if store.eventsCaseID != caseID {
		t.Errorf("ListCaseAuditEvents case ID = %s, want %s", store.eventsCaseID.String(), caseID.String())
	}
}

func TestGetCaseHistoryUnknownCase(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := historyStoreFor(actor)
	store.caseErr = pgx.ErrNoRows
	caseID := handlerTestUUID(4)
	request := caseRequestWithID(http.MethodGet, "/api/v1/cases/"+caseID.String()+"/history", caseID.String(), "", &actor)
	response := httptest.NewRecorder()

	handler.GetCaseHistory(store).ServeHTTP(response, request)

	assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
	if store.participantCalls != 0 || store.eventsCalls != 0 {
		t.Errorf("participant/history calls = %d/%d, want 0/0", store.participantCalls, store.eventsCalls)
	}
}

func TestGetCaseHistoryConcealsCaseFromNonParticipant(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := historyStoreFor(actor)
	store.participants = []db.CaseParticipant{{
		ID: handlerTestUUID(30), CaseID: handlerTestUUID(4), UserID: handlerTestUUID(8), Role: "CHECKER", Status: "ACTIVE",
	}}
	caseID := handlerTestUUID(4)
	request := caseRequestWithID(http.MethodGet, "/api/v1/cases/"+caseID.String()+"/history", caseID.String(), "", &actor)
	response := httptest.NewRecorder()

	handler.GetCaseHistory(store).ServeHTTP(response, request)

	assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
	if store.eventsCalls != 0 {
		t.Errorf("ListCaseAuditEvents calls = %d, want 0", store.eventsCalls)
	}
}

func TestGetCaseHistoryPreservesAnalysisFailureType(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	for _, failureType := range []string{"VERIFIER_FAIL", "TECHNICAL_RETRY_EXHAUSTED"} {
		t.Run(failureType, func(t *testing.T) {
			store := historyStoreFor(actor, db.AuditEvent{
				ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: "AI_ANALYSIS_FAILED",
				Metadata: []byte(`{"failure_type":"` + failureType + `","raw_response":"provider output"}`), CreatedAt: caseTestTime,
			})

			_, items := getHistory(t, store, actor)
			if len(items) != 1 || items[0].Metadata["failure_type"] != failureType {
				t.Fatalf("history = %+v, want failure_type %s", items, failureType)
			}
			if len(items[0].Metadata) != 1 {
				t.Errorf("metadata = %v, want only failure_type", items[0].Metadata)
			}
		})
	}
}

func TestGetCaseHistoryPreservesReanalysisLimit(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := historyStoreFor(actor, db.AuditEvent{
		ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: "REANALYSIS_LIMIT_REACHED",
		Metadata: []byte(`{"latest_analysis_version":4,"max_reanalysis":3,"prompt":"hidden"}`), CreatedAt: caseTestTime,
	})

	_, items := getHistory(t, store, actor)
	if len(items) != 1 || items[0].Metadata["latest_analysis_version"] != float64(4) || items[0].Metadata["max_reanalysis"] != float64(3) {
		t.Fatalf("metadata = %v, want reanalysis counters", items[0].Metadata)
	}
	if len(items[0].Metadata) != 2 {
		t.Errorf("metadata = %v, want only reanalysis counters", items[0].Metadata)
	}
}

func TestGetCaseHistoryStripsSensitiveMetadata(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := historyStoreFor(actor, db.AuditEvent{
		ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: "CASE_UPDATED",
		Metadata: []byte(`{
			"field":"urgency",
			"prompt":"hidden prompt",
			"stack_trace":"hidden trace",
			"credentials":"hidden credentials",
			"raw_payload":"hidden payload",
			"nested":{"safe":"yes","token":"hidden token"}
		}`),
		CreatedAt: caseTestTime,
	})

	response, items := getHistory(t, store, actor)
	if len(items) != 1 {
		t.Fatalf("history length = %d, want 1; body=%s", len(items), response.Body.String())
	}
	for _, key := range []string{"prompt", "stack_trace", "credentials", "raw_payload"} {
		if _, exists := items[0].Metadata[key]; exists {
			t.Errorf("metadata contains sensitive key %q: %v", key, items[0].Metadata)
		}
	}
	if items[0].Metadata["field"] != "urgency" {
		t.Errorf("metadata = %v, want safe field preserved", items[0].Metadata)
	}
	nested, ok := items[0].Metadata["nested"].(map[string]any)
	if !ok || nested["safe"] != "yes" {
		t.Fatalf("nested metadata = %v, want safe value", items[0].Metadata["nested"])
	}
	if _, exists := nested["token"]; exists {
		t.Errorf("nested metadata contains token: %v", nested)
	}
	for _, secret := range []string{"hidden prompt", "hidden trace", "hidden credentials", "hidden payload", "hidden token"} {
		if bytes := response.Body.String(); strings.Contains(bytes, secret) {
			t.Errorf("response leaked %q: %s", secret, bytes)
		}
	}
}

func TestGetCaseHistorySystemEventHasNullActor(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	store := historyStoreFor(actor, db.AuditEvent{
		ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: "AI_ANALYSIS_STARTED",
		Metadata: []byte(`{}`), CreatedAt: caseTestTime,
	})

	response, items := getHistory(t, store, actor)
	if response.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("GET history = %d, items=%d; body=%s", response.Code, len(items), response.Body.String())
	}
	if items[0].ActorID != nil || items[0].ActorRole != nil {
		t.Errorf("actor = %v/%v, want null/null", items[0].ActorID, items[0].ActorRole)
	}
}

func TestGetCaseHistoryPopulatesAnalysisVersion(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	analysisID := handlerTestUUID(6)
	store := historyStoreFor(actor, db.AuditEvent{
		ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: "CHECKER_APPROVED",
		AnalysisID: analysisID, Metadata: []byte(`{"analysis_id":"` + analysisID.String() + `","decision":"APPROVE"}`), CreatedAt: caseTestTime,
	})
	store.analyses[analysisID] = db.AiAnalysis{ID: analysisID, CaseID: handlerTestUUID(4), Version: 7}

	_, items := getHistory(t, store, actor)
	if len(items) != 1 || items[0].AnalysisVersion == nil || *items[0].AnalysisVersion != 7 {
		t.Fatalf("history = %+v, want analysis version 7", items)
	}
	if store.analysisCalls != 1 {
		t.Errorf("GetAnalysis calls = %d, want 1", store.analysisCalls)
	}
}

func TestGetCaseHistoryUsesEventMetadataAllowlists(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	tests := []struct {
		name      string
		eventType string
		metadata  string
		wantKeys  []string
	}{
		{"decision", "SIGNER_APPROVED", `{"analysis_id":"a","decision":"APPROVE","evidence_ids":["e"],"decision_snapshot":{"recommendation":"hidden"},"comment":"hidden"}`, []string{"analysis_id", "decision", "evidence_ids"}},
		{"execution", "EXECUTION_SUCCESS", `{"execution_id":"e","outcome":"SUCCESS","action_taken":"done","result":"ok","blocker":"hidden"}`, []string{"execution_id", "outcome", "action_taken", "result"}},
		{"evidence", "EVIDENCE_ADDED", `{"evidence_id":"e","evidence_type":"DOCUMENT","title":"safe","content":"hidden","file_path":"hidden"}`, []string{"evidence_id", "evidence_type", "title"}},
		{"participant", "PARTICIPANT_ASSIGNED", `{"user_id":"u","role":"CHECKER","required":true}`, []string{"user_id", "role"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := historyStoreFor(actor, db.AuditEvent{
				ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), EventType: tt.eventType,
				Metadata: []byte(tt.metadata), CreatedAt: caseTestTime,
			})
			_, items := getHistory(t, store, actor)
			if len(items) != 1 {
				t.Fatalf("history length = %d, want 1", len(items))
			}
			if len(items[0].Metadata) != len(tt.wantKeys) {
				t.Fatalf("metadata = %v, want keys %v", items[0].Metadata, tt.wantKeys)
			}
			for _, key := range tt.wantKeys {
				if _, exists := items[0].Metadata[key]; !exists {
					t.Errorf("metadata = %v, missing %q", items[0].Metadata, key)
				}
			}
		})
	}
}

func TestGetCaseHistoryAdminBypassesParticipantCheck(t *testing.T) {
	admin := caseTestUser(auth.SystemRoleAdmin)
	store := historyStoreFor(admin)
	store.participants = nil

	response, items := getHistory(t, store, admin)
	if response.Code != http.StatusOK || len(items) != 0 {
		t.Fatalf("GET history = %d, items=%d; body=%s", response.Code, len(items), response.Body.String())
	}
	if store.participantCalls != 0 {
		t.Errorf("ListCaseParticipants calls = %d, want 0 for admin", store.participantCalls)
	}
}
