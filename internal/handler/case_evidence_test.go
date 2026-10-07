package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakeEvidenceTxQueries struct {
	caseResult   db.Case
	participants []db.CaseParticipant
	evidences    []db.CaseEvidence
	userNames    map[pgtype.UUID]string

	getCaseCalls         int
	listParticipantCalls int
	createCalls          int
	createArg            db.CreateEvidenceParams
	listEvidenceCalls    int
	getUserCalls         int
	auditCalls           int
	auditArgs            []db.AppendCaseAuditEventParams
}

var _ handler.EvidenceTxQueries = (*fakeEvidenceTxQueries)(nil)

func (f *fakeEvidenceTxQueries) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	f.getCaseCalls++
	return f.caseResult, nil
}

func (f *fakeEvidenceTxQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	f.listParticipantCalls++
	return f.participants, nil
}

func (f *fakeEvidenceTxQueries) CreateEvidence(_ context.Context, arg db.CreateEvidenceParams) (db.CaseEvidence, error) {
	f.createCalls++
	f.createArg = arg
	return db.CaseEvidence{
		ID:           arg.ID,
		CaseID:       arg.CaseID,
		SourceType:   arg.SourceType,
		SourceUserID: arg.SourceUserID,
		EvidenceType: arg.EvidenceType,
		Title:        arg.Title,
		Content:      arg.Content,
		FilePath:     arg.FilePath,
		MimeType:     arg.MimeType,
		CreatedAt:    caseTestTime,
	}, nil
}

func (f *fakeEvidenceTxQueries) ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error) {
	f.listEvidenceCalls++
	return f.evidences, nil
}

func (f *fakeEvidenceTxQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArgs = append(f.auditArgs, arg)
	return db.AuditEvent{}, nil
}

func (f *fakeEvidenceTxQueries) GetUser(_ context.Context, id pgtype.UUID) (db.User, error) {
	f.getUserCalls++
	return db.User{ID: id, Name: f.userNames[id]}, nil
}

type fakeEvidenceStore struct {
	queries *fakeEvidenceTxQueries
	calls   int
}

var _ handler.EvidenceStore = (*fakeEvidenceStore)(nil)

func (f *fakeEvidenceStore) RunEvidenceTx(ctx context.Context, fn func(context.Context, handler.EvidenceTxQueries) error) error {
	f.calls++
	return fn(ctx, f.queries)
}

func evidenceTestQueries(actor auth.User, role, status string) *fakeEvidenceTxQueries {
	caseID := handlerTestUUID(4)
	return &fakeEvidenceTxQueries{
		caseResult: db.Case{
			ID:         caseID,
			CaseNumber: "CASE-X",
			CaseTypeID: handlerTestUUID(3),
			Title:      "Leaking pipe",
			Urgency:    "MEDIUM",
			Status:     status,
			CreatedBy:  actor.ID,
			OwnerID:    actor.ID,
			CreatedAt:  caseTestTime,
			UpdatedAt:  caseTestTime,
		},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: role, Required: true, Status: "ACTIVE"},
		},
		userNames: map[pgtype.UUID]string{actor.ID: "Evidence Actor"},
	}
}

func serveCaseEvidence(method string, actor auth.User, queries *fakeEvidenceTxQueries, body string) (*httptest.ResponseRecorder, *fakeEvidenceStore) {
	store := &fakeEvidenceStore{queries: queries}
	request := caseRequestWithID(
		method,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/evidences",
		handlerTestUUID(4).String(),
		body,
		&actor,
	)
	response := httptest.NewRecorder()
	if method == http.MethodGet {
		handler.ListCaseEvidences(store).ServeHTTP(response, request)
	} else {
		handler.AddCaseEvidence(store).ServeHTTP(response, request)
	}
	return response, store
}

const validEvidenceBody = `{"evidence_type":"COMMENT","title":" Additional context ","content":" Upstream batch arrived late. "}`

func TestAddCaseEvidenceMakerAtDraft(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
	originalStatus := queries.caseResult.Status

	response, store := serveCaseEvidence(http.MethodPost, actor, queries, validEvidenceBody)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			ID           string `json:"id"`
			EvidenceType string `json:"evidence_type"`
			SourceType   string `json:"source_type"`
			SourceUser   struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"source_user"`
			Title     *string `json:"title"`
			Content   *string `json:"content"`
			FilePath  *string `json:"file_path"`
			MimeType  *string `json:"mime_type"`
			CreatedAt string  `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.SourceType != "MAKER" || body.Data.SourceUser.ID != actor.ID.String() || body.Data.SourceUser.Name != "Evidence Actor" {
		t.Errorf("response attribution = %q/%q/%q, want MAKER/%q/Evidence Actor", body.Data.SourceType, body.Data.SourceUser.ID, body.Data.SourceUser.Name, actor.ID.String())
	}
	if body.Data.EvidenceType != "COMMENT" || body.Data.Title == nil || *body.Data.Title != "Additional context" || body.Data.Content == nil || *body.Data.Content != "Upstream batch arrived late." {
		t.Errorf("response evidence fields = %+v, want trimmed text evidence", body.Data)
	}
	if body.Data.FilePath != nil || body.Data.MimeType != nil {
		t.Errorf("response file fields = %v/%v, want null/null", body.Data.FilePath, body.Data.MimeType)
	}
	if queries.createCalls != 1 || queries.createArg.SourceType != "MAKER" || queries.createArg.SourceUserID != actor.ID {
		t.Errorf("CreateEvidence = calls %d arg %+v, want one call attributed to actor as MAKER", queries.createCalls, queries.createArg)
	}
	if !queries.createArg.Content.Valid || queries.createArg.FilePath.Valid || queries.createArg.MimeType.Valid {
		t.Errorf("text/file fields = content %v file %v mime %v, want non-NULL content and NULL file fields", queries.createArg.Content, queries.createArg.FilePath, queries.createArg.MimeType)
	}
	if queries.auditCalls != 1 || queries.auditArgs[0].EventType != "EVIDENCE_ADDED" || queries.auditArgs[0].ActorID != actor.ID || queries.auditArgs[0].ActorRole.String != "MAKER" {
		t.Fatalf("audit calls/arg = %d/%+v, want one EVIDENCE_ADDED by MAKER", queries.auditCalls, queries.auditArgs)
	}
	var metadata struct {
		EvidenceID string `json:"evidence_id"`
	}
	if err := json.Unmarshal(queries.auditArgs[0].Metadata, &metadata); err != nil || metadata.EvidenceID != queries.createArg.ID.String() {
		t.Errorf("audit metadata = %q (%v), want created evidence ID %q", queries.auditArgs[0].Metadata, err, queries.createArg.ID.String())
	}
	if queries.caseResult.Status != originalStatus {
		t.Errorf("case status = %q, want unchanged %q", queries.caseResult.Status, originalStatus)
	}
	if store.calls != 1 || queries.getCaseCalls != 1 || queries.listParticipantCalls != 1 || queries.listEvidenceCalls != 0 || queries.getUserCalls != 1 {
		t.Errorf("unexpected call counts: store=%d getCase=%d participants=%d create=%d listEvidence=%d getUser=%d audit=%d", store.calls, queries.getCaseCalls, queries.listParticipantCalls, queries.createCalls, queries.listEvidenceCalls, queries.getUserCalls, queries.auditCalls)
	}
}

func TestAddCaseEvidenceAllowedActiveParticipantStates(t *testing.T) {
	tests := []struct {
		name   string
		role   string
		status string
	}{
		{name: "checker at checking", role: "CHECKER", status: "CHECKING"},
		{name: "signer at signing", role: "SIGNER", status: "SIGNING"},
		{name: "executer at execution", role: "EXECUTER", status: "EXECUTION"},
		{name: "maker at escalation required", role: "MAKER", status: "ESCALATION_REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, tt.role, tt.status)

			response, _ := serveCaseEvidence(http.MethodPost, actor, queries, validEvidenceBody)

			if response.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
			}
			if queries.createCalls != 1 || queries.createArg.SourceType != tt.role || queries.auditCalls != 1 {
				t.Errorf("write calls/source = create %d source %q audit %d, want 1/%q/1", queries.createCalls, queries.createArg.SourceType, queries.auditCalls, tt.role)
			}
		})
	}
}

func TestAddCaseEvidenceForbiddenActorsAndStates(t *testing.T) {
	t.Run("checker cannot add at draft", func(t *testing.T) {
		actor := caseTestUser(auth.SystemRoleUser)
		queries := evidenceTestQueries(actor, "CHECKER", "DRAFT")
		response, _ := serveCaseEvidence(http.MethodPost, actor, queries, validEvidenceBody)
		assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
		assertNoEvidenceWrites(t, queries)
	})

	t.Run("unassigned user cannot post or get", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			t.Run(method, func(t *testing.T) {
				actor := caseTestUser(auth.SystemRoleUser)
				queries := evidenceTestQueries(actor, "MAKER", "CHECKING")
				queries.participants = nil
				response, _ := serveCaseEvidence(method, actor, queries, validEvidenceBody)
				assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
				assertNoEvidenceWrites(t, queries)
			})
		}
	})

	for _, status := range []string{"SUBMITTED", "AI_ANALYSIS", "DONE", "CLOSED"} {
		t.Run("forbidden at "+status, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, "MAKER", status)
			response, _ := serveCaseEvidence(http.MethodPost, actor, queries, validEvidenceBody)
			assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
			assertNoEvidenceWrites(t, queries)
		})
	}
}

func TestAddCaseEvidenceIgnoresClientAttribution(t *testing.T) {
	spoofedBody := `{"evidence_type":"COMMENT","title":"Context","content":"Late batch","actor_role":"MAKER","source_type":"SYSTEM"}`

	t.Run("unassigned remains forbidden", func(t *testing.T) {
		actor := caseTestUser(auth.SystemRoleUser)
		queries := evidenceTestQueries(actor, "MAKER", "CHECKING")
		queries.participants = nil
		response, _ := serveCaseEvidence(http.MethodPost, actor, queries, spoofedBody)
		assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
		assertNoEvidenceWrites(t, queries)
	})

	t.Run("checker attribution is server derived", func(t *testing.T) {
		actor := caseTestUser(auth.SystemRoleUser)
		queries := evidenceTestQueries(actor, "CHECKER", "CHECKING")
		response, _ := serveCaseEvidence(http.MethodPost, actor, queries, spoofedBody)
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		if queries.createArg.SourceType != "CHECKER" {
			t.Errorf("source_type = %q, want CHECKER", queries.createArg.SourceType)
		}
	})

	t.Run("multiple active roles are forbidden", func(t *testing.T) {
		actor := caseTestUser(auth.SystemRoleUser)
		queries := evidenceTestQueries(actor, "CHECKER", "CHECKING")
		queries.participants = append(queries.participants, db.CaseParticipant{
			ID: handlerTestUUID(11), CaseID: handlerTestUUID(4), UserID: actor.ID, Role: "SIGNER", Status: "ACTIVE",
		})
		response, _ := serveCaseEvidence(http.MethodPost, actor, queries, spoofedBody)
		assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
		assertNoEvidenceWrites(t, queries)
	})
}

func TestAddCaseEvidenceValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing title", body: `{"evidence_type":"COMMENT","content":"Context"}`},
		{name: "missing content", body: `{"evidence_type":"COMMENT","title":"Context"}`},
		{name: "invalid evidence type", body: `{"evidence_type":"BINARY","title":"Context","content":"Details"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
			response, store := serveCaseEvidence(http.MethodPost, actor, queries, tt.body)
			assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
			if store.calls != 0 {
				t.Errorf("transaction calls = %d, want 0", store.calls)
			}
			assertNoEvidenceWrites(t, queries)
		})
	}
}

func TestListCaseEvidencesActiveParticipantInDone(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := evidenceTestQueries(actor, "CHECKER", "DONE")
	firstUserID := handlerTestUUID(5)
	secondUserID := handlerTestUUID(6)
	queries.userNames[firstUserID] = "Maker One"
	queries.userNames[secondUserID] = "Checker Two"
	queries.evidences = []db.CaseEvidence{
		{
			ID: handlerTestUUID(20), CaseID: handlerTestUUID(4), SourceType: "MAKER", SourceUserID: firstUserID,
			EvidenceType: "COMMENT", Title: pgtype.Text{String: "First", Valid: true}, Content: pgtype.Text{String: "Earlier", Valid: true}, CreatedAt: caseTestTime,
		},
		{
			ID: handlerTestUUID(21), CaseID: handlerTestUUID(4), SourceType: "CHECKER", SourceUserID: secondUserID,
			EvidenceType: "LOG", Title: pgtype.Text{String: "Second", Valid: true}, Content: pgtype.Text{String: "Later", Valid: true}, CreatedAt: caseTestTime.Add(time.Minute),
		},
	}

	response, store := serveCaseEvidence(http.MethodGet, actor, queries, "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []struct {
			ID         string `json:"id"`
			SourceType string `json:"source_type"`
			SourceUser struct {
				ID string `json:"id"`
			} `json:"source_user"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 2 || body.Data[0].ID != handlerTestUUID(20).String() || body.Data[1].ID != handlerTestUUID(21).String() || !body.Data[0].CreatedAt.Before(body.Data[1].CreatedAt) {
		t.Fatalf("evidence order = %+v, want chronological IDs 20 then 21", body.Data)
	}
	if body.Data[0].SourceType != "MAKER" || body.Data[0].SourceUser.ID != firstUserID.String() || body.Data[1].SourceType != "CHECKER" || body.Data[1].SourceUser.ID != secondUserID.String() {
		t.Errorf("source attribution = %+v, want stored source roles and users", body.Data)
	}
	if store.calls != 1 || queries.getCaseCalls != 1 || queries.listParticipantCalls != 1 || queries.listEvidenceCalls != 1 || queries.getUserCalls != 2 {
		t.Errorf("unexpected read call counts: store=%d case=%d participants=%d evidences=%d users=%d", store.calls, queries.getCaseCalls, queries.listParticipantCalls, queries.listEvidenceCalls, queries.getUserCalls)
	}
	if queries.createCalls != 0 || queries.auditCalls != 0 {
		t.Errorf("GET write calls = create %d audit %d, want zero", queries.createCalls, queries.auditCalls)
	}
}

func assertNoEvidenceWrites(t *testing.T, queries *fakeEvidenceTxQueries) {
	t.Helper()
	if queries.createCalls != 0 || queries.auditCalls != 0 {
		t.Errorf("evidence write calls = create %d audit %d, want zero", queries.createCalls, queries.auditCalls)
	}
}
