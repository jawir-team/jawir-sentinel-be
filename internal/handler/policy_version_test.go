package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakePolicyVersionStore struct {
	policy       db.Policy
	policyErr    error
	version      db.PolicyVersion
	versionErr   error
	versions     []db.PolicyVersion
	listErr      error
	created      db.PolicyVersion
	createErr    error
	auditErr     error
	policyCalls  int
	versionCalls int
	listCalls    int
	createCalls  int
	auditCalls   int
	gotPolicyID  pgtype.UUID
	gotVersionID pgtype.UUID
	gotListID    pgtype.UUID
	createArg    db.CreatePolicyVersionParams
	auditArg     db.AppendPolicyAuditEventParams
}

func (f *fakePolicyVersionStore) GetPolicy(_ context.Context, id pgtype.UUID) (db.Policy, error) {
	f.policyCalls++
	f.gotPolicyID = id
	return f.policy, f.policyErr
}

func (f *fakePolicyVersionStore) GetPolicyVersion(_ context.Context, id pgtype.UUID) (db.PolicyVersion, error) {
	f.versionCalls++
	f.gotVersionID = id
	return f.version, f.versionErr
}

func (f *fakePolicyVersionStore) ListPolicyVersions(_ context.Context, id pgtype.UUID) ([]db.PolicyVersion, error) {
	f.listCalls++
	f.gotListID = id
	return f.versions, f.listErr
}

func (f *fakePolicyVersionStore) CreatePolicyVersion(_ context.Context, arg db.CreatePolicyVersionParams) (db.PolicyVersion, error) {
	f.createCalls++
	f.createArg = arg
	return f.created, f.createErr
}

func (f *fakePolicyVersionStore) AppendPolicyAuditEvent(_ context.Context, arg db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, f.auditErr
}

func TestCreatePolicyVersionAsAdmin(t *testing.T) {
	policyID := handlerTestUUID(1)
	versionID := handlerTestUUID(2)
	actorID := handlerTestUUID(9)
	createdAt := time.Date(2026, time.October, 6, 10, 0, 0, 0, time.UTC)
	store := &fakePolicyVersionStore{
		policy: db.Policy{ID: policyID},
		created: db.PolicyVersion{
			ID:          versionID,
			PolicyID:    policyID,
			Version:     "v1",
			Status:      "DRAFT",
			IndexStatus: "NOT_STARTED",
			Content:     "Policy body",
			CreatedBy:   actorID,
			CreatedAt:   createdAt,
		},
	}

	response := serveCreatePolicyVersion(t, store, policyID,
		`{"version":"  v1  ","content":"Policy body","file_path":""}`,
	)

	assertJSONResponse(t, response, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":                versionID.String(),
			"policy_id":         policyID.String(),
			"version":           "v1",
			"status":            "DRAFT",
			"content":           "Policy body",
			"file_path":         nil,
			"effective_from":    nil,
			"effective_until":   nil,
			"index_status":      "NOT_STARTED",
			"index_error":       nil,
			"index_attempt_id":  nil,
			"index_started_at":  nil,
			"indexed_at":        nil,
			"created_by":        actorID.String(),
			"created_at":        "2026-10-06T10:00:00Z",
			"index_recoverable": false,
		},
	})
	if store.createCalls != 1 {
		t.Fatalf("CreatePolicyVersion calls = %d, want 1", store.createCalls)
	}
	if !store.createArg.ID.Valid || store.createArg.ID.Bytes == ([16]byte{}) {
		t.Errorf("CreatePolicyVersion ID = %+v, want a generated UUID", store.createArg.ID)
	}
	wantCreateArg := db.CreatePolicyVersionParams{
		ID:        store.createArg.ID,
		PolicyID:  policyID,
		Version:   "v1",
		Content:   "Policy body",
		CreatedBy: actorID,
	}
	if !reflect.DeepEqual(store.createArg, wantCreateArg) {
		t.Errorf("CreatePolicyVersion arg = %+v, want %+v", store.createArg, wantCreateArg)
	}
	if store.created.Status != "DRAFT" || store.created.IndexStatus != "NOT_STARTED" {
		t.Errorf("database defaults = status %q, index_status %q", store.created.Status, store.created.IndexStatus)
	}
	if store.created.IndexAttemptID.Valid || store.created.IndexStartedAt.Valid || store.created.IndexedAt.Valid || store.created.IndexError.Valid {
		t.Errorf("new version indexing fields should all be NULL: %+v", store.created)
	}
	if store.auditCalls != 1 {
		t.Fatalf("AppendPolicyAuditEvent calls = %d, want 1", store.auditCalls)
	}
	if !store.auditArg.ID.Valid || store.auditArg.ID.Bytes == ([16]byte{}) {
		t.Errorf("audit ID = %+v, want a generated UUID", store.auditArg.ID)
	}
	wantAuditArg := db.AppendPolicyAuditEventParams{
		ID:              store.auditArg.ID,
		PolicyID:        policyID,
		PolicyVersionID: versionID,
		EventType:       "POLICY_VERSION_CREATED",
		ActorID:         actorID,
		Metadata:        []byte(`{}`),
	}
	if !reflect.DeepEqual(store.auditArg, wantAuditArg) {
		t.Errorf("AppendPolicyAuditEvent arg = %+v, want %+v", store.auditArg, wantAuditArg)
	}
}

func TestCreatePolicyVersionReturnsConflictForDuplicateVersion(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{
		policy: db.Policy{ID: policyID},
		createErr: &pgconn.PgError{
			Code:           "23505",
			ConstraintName: "policy_versions_version_unique",
		},
	}
	response := serveCreatePolicyVersion(t, store, policyID, `{"version":"v1","content":"Duplicate"}`)

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "")
	if store.auditCalls != 0 {
		t.Errorf("AppendPolicyAuditEvent calls = %d, want 0", store.auditCalls)
	}
}

func TestCreatePolicyVersionRejectsInvalidEffectiveRange(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{policy: db.Policy{ID: policyID}}
	response := serveCreatePolicyVersion(t, store, policyID,
		`{"version":"v1","content":"Body","effective_from":"2026-11-01T00:00:00Z","effective_until":"2026-11-01T00:00:00Z"}`,
	)

	assertPolicyAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError,
		"effective_until must be after effective_from")
	if store.createCalls != 0 {
		t.Errorf("CreatePolicyVersion calls = %d, want 0", store.createCalls)
	}
}

func TestCreatePolicyVersionAllowsFutureEffectiveDraft(t *testing.T) {
	policyID := handlerTestUUID(1)
	actorID := handlerTestUUID(9)
	effectiveFrom := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	store := &fakePolicyVersionStore{
		policy: db.Policy{ID: policyID},
		created: db.PolicyVersion{
			ID:            handlerTestUUID(2),
			PolicyID:      policyID,
			Version:       "future",
			Status:        "DRAFT",
			IndexStatus:   "NOT_STARTED",
			Content:       "Future body",
			EffectiveFrom: pgtype.Timestamptz{Time: effectiveFrom, Valid: true},
			CreatedBy:     actorID,
			CreatedAt:     time.Date(2026, time.October, 6, 10, 0, 0, 0, time.UTC),
		},
	}
	response := serveCreatePolicyVersion(t, store, policyID,
		`{"version":"future","content":"Future body","effective_from":"2030-01-01T00:00:00Z"}`,
	)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	if !store.createArg.EffectiveFrom.Valid || !store.createArg.EffectiveFrom.Time.Equal(effectiveFrom) {
		t.Errorf("effective_from = %+v, want %s", store.createArg.EffectiveFrom, effectiveFrom)
	}
}

func TestCreatePolicyVersionRejectsInvalidEffectiveFrom(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{policy: db.Policy{ID: policyID}}
	response := serveCreatePolicyVersion(t, store, policyID,
		`{"version":"v1","content":"Body","effective_from":"tomorrow"}`,
	)

	assertPolicyAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError, "")
	if store.createCalls != 0 {
		t.Errorf("CreatePolicyVersion calls = %d, want 0", store.createCalls)
	}
}

func TestCreatePolicyVersionReturnsNotFoundForUnknownPolicy(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{policyErr: pgx.ErrNoRows}
	response := serveCreatePolicyVersion(t, store, policyID, `{"version":"v1","content":"Body"}`)

	assertPolicyAPIError(t, response, http.StatusNotFound, httpapi.CodePolicyNotFound, "")
	if store.createCalls != 0 || store.auditCalls != 0 {
		t.Errorf("store calls = create %d, audit %d; want zero", store.createCalls, store.auditCalls)
	}
}

func TestGetPolicyVersionIndexRecoverable(t *testing.T) {
	t.Setenv("POLICY_INDEX_LEASE_SECONDS", "900")
	policyID := handlerTestUUID(1)
	now := time.Now()
	tests := []struct {
		name        string
		status      string
		startedAt   pgtype.Timestamptz
		recoverable bool
	}{
		{
			name:        "processing past lease",
			status:      "PROCESSING",
			startedAt:   pgtype.Timestamptz{Time: now.Add(-20 * time.Minute), Valid: true},
			recoverable: true,
		},
		{
			name:        "processing within lease",
			status:      "PROCESSING",
			startedAt:   pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
			recoverable: false,
		},
		{
			name:        "draft past lease",
			status:      "NOT_STARTED",
			startedAt:   pgtype.Timestamptz{Time: now.Add(-20 * time.Minute), Valid: true},
			recoverable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePolicyVersionStore{
				policy: db.Policy{ID: policyID},
				version: db.PolicyVersion{
					ID:             handlerTestUUID(2),
					PolicyID:       policyID,
					Version:        "v1",
					Status:         "DRAFT",
					IndexStatus:    tt.status,
					IndexStartedAt: tt.startedAt,
					Content:        "Body",
					CreatedBy:      handlerTestUUID(9),
					CreatedAt:      now,
				},
			}
			response := serveGetPolicyVersion(t, store, policyID, handlerTestUUID(2))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Data struct {
					IndexRecoverable bool `json:"index_recoverable"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Data.IndexRecoverable != tt.recoverable {
				t.Errorf("index_recoverable = %t, want %t", body.Data.IndexRecoverable, tt.recoverable)
			}
		})
	}
}

func TestListPolicyVersionsPreservesStoreOrder(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{
		policy: db.Policy{ID: policyID},
		versions: []db.PolicyVersion{
			{ID: handlerTestUUID(2), PolicyID: policyID, Version: "v1", Status: "DRAFT", IndexStatus: "NOT_STARTED", CreatedBy: handlerTestUUID(9)},
			{ID: handlerTestUUID(3), PolicyID: policyID, Version: "v2", Status: "DRAFT", IndexStatus: "NOT_STARTED", CreatedBy: handlerTestUUID(9)},
		},
	}
	response := serveListPolicyVersions(t, store, policyID)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 2 || body.Data[0].ID != handlerTestUUID(2).String() || body.Data[1].ID != handlerTestUUID(3).String() {
		t.Errorf("version order = %+v, want IDs 2 then 3", body.Data)
	}
	if store.listCalls != 1 || store.gotListID != policyID {
		t.Errorf("ListPolicyVersions calls = %d, ID = %v; want 1, %v", store.listCalls, store.gotListID, policyID)
	}
}

func TestGetPolicyVersionHidesVersionFromAnotherPolicy(t *testing.T) {
	policyID := handlerTestUUID(1)
	store := &fakePolicyVersionStore{
		policy: db.Policy{ID: policyID},
		version: db.PolicyVersion{
			ID:       handlerTestUUID(3),
			PolicyID: handlerTestUUID(2),
		},
	}
	response := serveGetPolicyVersion(t, store, policyID, handlerTestUUID(3))

	assertPolicyAPIError(t, response, http.StatusNotFound, httpapi.CodePolicyNotFound, "")
}

func serveCreatePolicyVersion(t *testing.T, store *fakePolicyVersionStore, policyID pgtype.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.With(auth.RequireAdmin).Post("/api/v1/policies/{id}/versions", handler.CreatePolicyVersion(store))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/policies/"+policyID.String()+"/versions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func serveGetPolicyVersion(t *testing.T, store *fakePolicyVersionStore, policyID, versionID pgtype.UUID) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.With(auth.RequireAuth).Get("/api/v1/policies/{id}/versions/{version_id}", handler.GetPolicyVersion(store))
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/policies/"+policyID.String()+"/versions/"+versionID.String(), nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(8),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func serveListPolicyVersions(t *testing.T, store *fakePolicyVersionStore, policyID pgtype.UUID) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.With(auth.RequireAuth).Get("/api/v1/policies/{id}/versions", handler.ListPolicyVersions(store))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/policies/"+policyID.String()+"/versions", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(8),
		SystemRole: auth.SystemRoleUser,
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
