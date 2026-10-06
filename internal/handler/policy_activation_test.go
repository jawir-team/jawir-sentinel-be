package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

type fakePolicyActivationTxQueries struct {
	version    db.PolicyVersion
	versionErr error
	active     db.PolicyVersion
	activeErr  error

	claimArgs    []db.ClaimPolicyVersionIndexParams
	claimErr     error
	completeArgs []db.CompletePolicyVersionIndexParams
	completeErr  error
	updateArgs   []db.UpdatePolicyVersionStatusParams
	updateErr    error
	auditArgs    []db.AppendPolicyAuditEventParams
	auditErr     error

	activeCalls int
}

var _ handler.PolicyActivationTxQueries = (*fakePolicyActivationTxQueries)(nil)

func (f *fakePolicyActivationTxQueries) GetPolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error) {
	return f.version, f.versionErr
}

func (f *fakePolicyActivationTxQueries) GetActivePolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error) {
	f.activeCalls++
	return f.active, f.activeErr
}

func (f *fakePolicyActivationTxQueries) ClaimPolicyVersionIndex(_ context.Context, arg db.ClaimPolicyVersionIndexParams) (db.PolicyVersion, error) {
	f.claimArgs = append(f.claimArgs, arg)
	if f.claimErr != nil {
		return db.PolicyVersion{}, f.claimErr
	}
	f.version.IndexStatus = "PROCESSING"
	f.version.IndexAttemptID = arg.IndexAttemptID
	f.version.IndexStartedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.version.IndexError = pgtype.Text{}
	f.version.IndexedAt = pgtype.Timestamptz{}
	return f.version, nil
}

func (f *fakePolicyActivationTxQueries) CompletePolicyVersionIndex(_ context.Context, arg db.CompletePolicyVersionIndexParams) (db.PolicyVersion, error) {
	f.completeArgs = append(f.completeArgs, arg)
	if f.completeErr != nil {
		return db.PolicyVersion{}, f.completeErr
	}
	if arg.IndexAttemptID != f.version.IndexAttemptID {
		return db.PolicyVersion{}, pgx.ErrNoRows
	}
	f.version.IndexStatus = arg.IndexStatus
	f.version.IndexError = arg.IndexError
	if arg.IndexStatus == "READY" {
		f.version.IndexedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return f.version, nil
}

func (f *fakePolicyActivationTxQueries) UpdatePolicyVersionStatus(_ context.Context, arg db.UpdatePolicyVersionStatusParams) (db.PolicyVersion, error) {
	f.updateArgs = append(f.updateArgs, arg)
	if f.updateErr != nil {
		return db.PolicyVersion{}, f.updateErr
	}
	if arg.ID == f.active.ID {
		f.active.Status = arg.Status
		return f.active, nil
	}
	f.version.Status = arg.Status
	return f.version, nil
}

func (f *fakePolicyActivationTxQueries) AppendPolicyAuditEvent(_ context.Context, arg db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	f.auditArgs = append(f.auditArgs, arg)
	return db.AuditEvent{}, f.auditErr
}

type fakePolicyActivationStore struct {
	queries *fakePolicyActivationTxQueries
	calls   int
	txErr   error
}

var _ handler.PolicyActivationStore = (*fakePolicyActivationStore)(nil)

func (f *fakePolicyActivationStore) RunPolicyActivationTx(ctx context.Context, fn func(context.Context, handler.PolicyActivationTxQueries) error) error {
	f.calls++
	if err := fn(ctx, f.queries); err != nil {
		return err
	}
	return f.txErr
}

func newPolicyActivationQueries(indexStatus string) *fakePolicyActivationTxQueries {
	return &fakePolicyActivationTxQueries{
		version: db.PolicyVersion{
			ID:          handlerTestUUID(2),
			PolicyID:    handlerTestUUID(1),
			Version:     "v2",
			Status:      "DRAFT",
			IndexStatus: indexStatus,
			Content:     "Policy body",
			CreatedBy:   handlerTestUUID(8),
			CreatedAt:   time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC),
		},
		activeErr: pgx.ErrNoRows,
	}
}

func TestClaimPolicyVersionIndexRejectsFutureEffectiveDraft(t *testing.T) {
	queries := newPolicyActivationQueries("NOT_STARTED")
	queries.version.EffectiveFrom = pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}

	response := servePolicyActivation(t, queries, "claim-index", "")

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "not yet effective")
	if len(queries.claimArgs) != 0 || len(queries.auditArgs) != 0 {
		t.Fatalf("side effects = claims %d, audits %d; want zero", len(queries.claimArgs), len(queries.auditArgs))
	}
}

func TestClaimPolicyVersionIndexClaimsNotStartedAndFailed(t *testing.T) {
	for _, status := range []string{"NOT_STARTED", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			queries := newPolicyActivationQueries(status)
			if status == "FAILED" {
				queries.version.IndexError = pgtype.Text{String: "previous failure", Valid: true}
			}

			response := servePolicyActivation(t, queries, "claim-index", "")

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
			}
			if len(queries.claimArgs) != 1 || !queries.claimArgs[0].IndexAttemptID.Valid {
				t.Fatalf("claim args = %+v, want one generated attempt", queries.claimArgs)
			}
			var body struct {
				Data struct {
					IndexStatus    string `json:"index_status"`
					IndexAttemptID string `json:"index_attempt_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.IndexStatus != "PROCESSING" || body.Data.IndexAttemptID != queries.claimArgs[0].IndexAttemptID.String() {
				t.Errorf("response index = %+v, want PROCESSING and claimed attempt", body.Data)
			}
			if len(queries.auditArgs) != 1 || queries.auditArgs[0].EventType != "POLICY_INDEX_CLAIMED" {
				t.Errorf("audits = %+v, want POLICY_INDEX_CLAIMED", queries.auditArgs)
			}
		})
	}
}

func TestClaimPolicyVersionIndexProcessingLease(t *testing.T) {
	t.Setenv("POLICY_INDEX_LEASE_SECONDS", "900")

	t.Run("non-stale", func(t *testing.T) {
		queries := newPolicyActivationQueries("PROCESSING")
		queries.version.IndexAttemptID = handlerTestUUID(3)
		queries.version.IndexStartedAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}

		response := servePolicyActivation(t, queries, "claim-index", "")

		assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "indexing already in progress")
		if len(queries.claimArgs) != 0 {
			t.Fatalf("claims = %d, want zero", len(queries.claimArgs))
		}
	})

	t.Run("stale", func(t *testing.T) {
		queries := newPolicyActivationQueries("PROCESSING")
		oldAttempt := handlerTestUUID(3)
		queries.version.IndexAttemptID = oldAttempt
		queries.version.IndexStartedAt = pgtype.Timestamptz{Time: time.Now().Add(-20 * time.Minute), Valid: true}

		response := servePolicyActivation(t, queries, "claim-index", "")

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
		}
		if len(queries.claimArgs) != 1 || queries.claimArgs[0].IndexAttemptID == oldAttempt {
			t.Fatalf("claim args = %+v, want one new attempt distinct from %s", queries.claimArgs, oldAttempt.String())
		}
	})
}

func TestCompletePolicyVersionIndexAttemptGuard(t *testing.T) {
	t.Run("wrong attempt", func(t *testing.T) {
		queries := newPolicyActivationQueries("PROCESSING")
		queries.version.IndexAttemptID = handlerTestUUID(3)

		response := servePolicyActivation(t, queries, "index-result", `{"index_attempt_id":"00000000-0000-0000-0000-000000000004","index_status":"READY"}`)

		assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "stale attempt superseded")
		if queries.version.IndexStatus != "PROCESSING" {
			t.Fatalf("index status = %q, want PROCESSING unchanged", queries.version.IndexStatus)
		}
		if len(queries.completeArgs) != 1 || len(queries.auditArgs) != 0 {
			t.Fatalf("calls = complete %d, audit %d; want 1, 0", len(queries.completeArgs), len(queries.auditArgs))
		}
	})

	t.Run("correct attempt", func(t *testing.T) {
		queries := newPolicyActivationQueries("PROCESSING")
		queries.version.IndexAttemptID = handlerTestUUID(3)

		response := servePolicyActivation(t, queries, "index-result", `{"index_attempt_id":"00000000-0000-0000-0000-000000000003","index_status":"READY"}`)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
		}
		var body struct {
			Data struct {
				IndexStatus string `json:"index_status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.IndexStatus != "READY" || queries.version.IndexStatus != "READY" {
			t.Fatalf("statuses = response %q, stored %q; want READY", body.Data.IndexStatus, queries.version.IndexStatus)
		}
		if len(queries.auditArgs) != 1 || queries.auditArgs[0].EventType != "POLICY_INDEX_COMPLETED" || string(queries.auditArgs[0].Metadata) != `{"index_status":"READY"}` {
			t.Errorf("audit = %+v, want completed READY metadata", queries.auditArgs)
		}
	})
}

func TestCompletePolicyVersionIndexRejectsNonProcessing(t *testing.T) {
	queries := newPolicyActivationQueries("READY")
	queries.version.IndexAttemptID = handlerTestUUID(3)

	response := servePolicyActivation(t, queries, "index-result", `{"index_attempt_id":"00000000-0000-0000-0000-000000000003","index_status":"FAILED","index_error":"worker failed"}`)

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "indexing is not in progress")
	if len(queries.completeArgs) != 0 {
		t.Fatalf("complete calls = %d, want zero", len(queries.completeArgs))
	}
}

func TestActivatePolicyVersionRejectsProcessingTarget(t *testing.T) {
	queries := newPolicyActivationQueries("PROCESSING")

	response := servePolicyActivation(t, queries, "activate", "")

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "policy version is not ready for activation")
	if queries.activeCalls != 0 || len(queries.updateArgs) != 0 {
		t.Fatalf("active lookups = %d, updates = %d; want zero", queries.activeCalls, len(queries.updateArgs))
	}
}

func TestActivatePolicyVersionRejectsExpiredBeforeTouchingActive(t *testing.T) {
	queries := newPolicyActivationQueries("READY")
	queries.version.IndexedAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	queries.version.EffectiveUntil = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	queries.active = db.PolicyVersion{ID: handlerTestUUID(3), PolicyID: handlerTestUUID(1), Status: "ACTIVE", IndexStatus: "READY"}
	queries.activeErr = nil

	response := servePolicyActivation(t, queries, "activate", "")

	assertPolicyAPIError(t, response, http.StatusConflict, httpapi.CodeConflict, "expired")
	if queries.activeCalls != 0 || len(queries.updateArgs) != 0 {
		t.Fatalf("active lookups = %d, updates = %d; want zero", queries.activeCalls, len(queries.updateArgs))
	}
	if queries.active.Status != "ACTIVE" {
		t.Errorf("old active status = %q, want ACTIVE", queries.active.Status)
	}
}

func TestActivatePolicyVersionSuccess(t *testing.T) {
	queries := newPolicyActivationQueries("READY")
	queries.version.IndexedAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	queries.active = db.PolicyVersion{ID: handlerTestUUID(3), PolicyID: handlerTestUUID(1), Status: "ACTIVE", IndexStatus: "READY"}
	queries.activeErr = nil

	response := servePolicyActivation(t, queries, "activate", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if len(queries.updateArgs) != 2 {
		t.Fatalf("updates = %+v, want old then target", queries.updateArgs)
	}
	if queries.updateArgs[0].ID != handlerTestUUID(3) || queries.updateArgs[0].Status != "SUPERSEDED" {
		t.Errorf("first update = %+v, want old SUPERSEDED", queries.updateArgs[0])
	}
	if queries.updateArgs[1].ID != handlerTestUUID(2) || queries.updateArgs[1].Status != "ACTIVE" {
		t.Errorf("second update = %+v, want target ACTIVE", queries.updateArgs[1])
	}
	if len(queries.auditArgs) != 2 || queries.auditArgs[0].EventType != "POLICY_VERSION_SUPERSEDED" || queries.auditArgs[1].EventType != "POLICY_VERSION_ACTIVATED" {
		t.Errorf("audits = %+v, want superseded then activated", queries.auditArgs)
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Status != "ACTIVE" {
		t.Errorf("response status = %q, want ACTIVE", body.Data.Status)
	}
}

func servePolicyActivation(t *testing.T, queries *fakePolicyActivationTxQueries, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	store := &fakePolicyActivationStore{queries: queries}
	router := chi.NewRouter()
	path := "/api/v1/policies/{id}/versions/{version_id}/" + action
	switch action {
	case "claim-index":
		router.With(auth.RequireAdmin).Post(path, handler.ClaimPolicyVersionIndex(store))
	case "index-result":
		router.With(auth.RequireAdmin).Post(path, handler.CompletePolicyVersionIndex(store))
	case "activate":
		router.With(auth.RequireAdmin).Post(path, handler.ActivatePolicyVersion(store))
	default:
		t.Fatalf("unknown policy activation action %q", action)
	}
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/policies/"+handlerTestUUID(1).String()+"/versions/"+handlerTestUUID(2).String()+"/"+action,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{
		ID:         handlerTestUUID(9),
		SystemRole: auth.SystemRoleAdmin,
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
