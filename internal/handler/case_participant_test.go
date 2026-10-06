package handler_test

import (
	"context"
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

type fakeParticipantTxQueries struct {
	caseResult db.Case
	caseErr    error

	participants []db.CaseParticipant
	listErr      error

	userResult db.User
	userErr    error

	participantByRole    db.CaseParticipant
	participantByRoleErr error

	activeParticipant    db.CaseParticipant
	activeParticipantErr error

	createResult db.CaseParticipant
	createErr    error
	createArg    db.CreateCaseParticipantParams
	createCalls  int

	reactivateResult db.CaseParticipant
	reactivateErr    error
	reactivateArg    db.ReactivateCaseParticipantParams
	reactivateCalls  int

	unassignResult db.CaseParticipant
	unassignErr    error
	unassignID     pgtype.UUID
	unassignCalls  int

	auditArg   db.AppendCaseAuditEventParams
	auditErr   error
	auditCalls int
}

var _ handler.ParticipantTxQueries = (*fakeParticipantTxQueries)(nil)

func (f *fakeParticipantTxQueries) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseResult, f.caseErr
}

func (f *fakeParticipantTxQueries) ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error) {
	return f.participants, f.listErr
}

func (f *fakeParticipantTxQueries) GetUserForUpdate(context.Context, pgtype.UUID) (db.User, error) {
	return f.userResult, f.userErr
}

func (f *fakeParticipantTxQueries) IsActiveCaseParticipant(context.Context, db.IsActiveCaseParticipantParams) (bool, error) {
	return false, nil
}

func (f *fakeParticipantTxQueries) GetParticipantByCaseUserRole(context.Context, db.GetParticipantByCaseUserRoleParams) (db.CaseParticipant, error) {
	if f.participantByRoleErr != nil {
		return db.CaseParticipant{}, f.participantByRoleErr
	}
	if !f.participantByRole.ID.Valid {
		return db.CaseParticipant{}, pgx.ErrNoRows
	}
	return f.participantByRole, nil
}

func (f *fakeParticipantTxQueries) ReactivateCaseParticipant(_ context.Context, arg db.ReactivateCaseParticipantParams) (db.CaseParticipant, error) {
	f.reactivateCalls++
	f.reactivateArg = arg
	return f.reactivateResult, f.reactivateErr
}

func (f *fakeParticipantTxQueries) CreateCaseParticipant(_ context.Context, arg db.CreateCaseParticipantParams) (db.CaseParticipant, error) {
	f.createCalls++
	f.createArg = arg
	if f.createErr != nil {
		return db.CaseParticipant{}, f.createErr
	}
	if f.createResult.ID.Valid {
		return f.createResult, nil
	}
	return db.CaseParticipant{
		ID:         arg.ID,
		CaseID:     arg.CaseID,
		UserID:     arg.UserID,
		Role:       arg.Role,
		Required:   arg.Required,
		Status:     "ACTIVE",
		AssignedBy: arg.AssignedBy,
		AssignedAt: participantTestTime,
	}, nil
}

func (f *fakeParticipantTxQueries) GetActiveParticipantForUpdate(context.Context, pgtype.UUID) (db.CaseParticipant, error) {
	return f.activeParticipant, f.activeParticipantErr
}

func (f *fakeParticipantTxQueries) UnassignCaseParticipant(_ context.Context, id pgtype.UUID) (db.CaseParticipant, error) {
	f.unassignCalls++
	f.unassignID = id
	return f.unassignResult, f.unassignErr
}

func (f *fakeParticipantTxQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.auditCalls++
	f.auditArg = arg
	return db.AuditEvent{}, f.auditErr
}

type fakeCaseParticipantStore struct {
	queries *fakeParticipantTxQueries
	calls   int
}

var _ handler.CaseParticipantStore = (*fakeCaseParticipantStore)(nil)

func (f *fakeCaseParticipantStore) RunParticipantTx(ctx context.Context, fn func(context.Context, handler.ParticipantTxQueries) error) error {
	f.calls++
	return fn(ctx, f.queries)
}

var participantTestTime = time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)

func participantTestQueries(actor auth.User) *fakeParticipantTxQueries {
	caseID := handlerTestUUID(4)
	return &fakeParticipantTxQueries{
		caseResult: db.Case{ID: caseID, Status: "DRAFT"},
		participants: []db.CaseParticipant{
			{ID: handlerTestUUID(10), CaseID: caseID, UserID: actor.ID, Role: "MAKER", Status: "ACTIVE"},
		},
		userResult: db.User{ID: handlerTestUUID(8), Status: "ACTIVE"},
	}
}

func participantRequest(method, body string, actor auth.User, participantID string) *http.Request {
	target := "/api/v1/cases/00000000-0000-0000-0000-000000000004/participants"
	if participantID != "" {
		target += "/" + participantID
	}
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", handlerTestUUID(4).String())
	if participantID != "" {
		routeCtx.URLParams.Add("participant_id", participantID)
	}
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx)
	return request.WithContext(auth.WithUser(ctx, actor))
}

func TestAssignCaseParticipant(t *testing.T) {
	actor := auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleUser}
	caseID := handlerTestUUID(4)
	targetUserID := handlerTestUUID(8)
	body := `{"user_id":"00000000-0000-0000-0000-000000000008","role":"CHECKER"}`

	t.Run("one active role per user returns conflict", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.participants = append(queries.participants, db.CaseParticipant{
			ID: handlerTestUUID(11), CaseID: caseID, UserID: targetUserID, Role: "CHECKER", Status: "ACTIVE",
		})
		response := serveAssignParticipant(actor, body, queries)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
		if queries.createCalls != 0 {
			t.Errorf("CreateCaseParticipant calls = %d, want 0", queries.createCalls)
		}
	})

	t.Run("multiple distinct checkers are allowed", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.participants = append(queries.participants, db.CaseParticipant{
			ID: handlerTestUUID(11), CaseID: caseID, UserID: handlerTestUUID(7), Role: "CHECKER", Status: "ACTIVE",
		})
		response := serveAssignParticipant(actor, body, queries)

		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		if queries.createCalls != 1 {
			t.Errorf("CreateCaseParticipant calls = %d, want 1", queries.createCalls)
		}
	})

	t.Run("second signer returns conflict", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.participants = append(queries.participants, db.CaseParticipant{
			ID: handlerTestUUID(11), CaseID: caseID, UserID: handlerTestUUID(7), Role: "SIGNER", Status: "ACTIVE",
		})
		signerBody := `{"user_id":"00000000-0000-0000-0000-000000000008","role":"SIGNER"}`
		response := serveAssignParticipant(actor, signerBody, queries)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	})

	t.Run("MAKER role is rejected", func(t *testing.T) {
		queries := participantTestQueries(actor)
		makerBody := `{"user_id":"00000000-0000-0000-0000-000000000008","role":"MAKER"}`
		response := serveAssignParticipant(actor, makerBody, queries)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	})

	t.Run("non-DRAFT case returns conflict", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.caseResult.Status = "SUBMITTED"
		response := serveAssignParticipant(actor, body, queries)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	})

	t.Run("inactive target user is rejected", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.userResult.Status = "INACTIVE"
		response := serveAssignParticipant(actor, body, queries)

		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	})

	t.Run("missing target user returns not found", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.userErr = pgx.ErrNoRows
		response := serveAssignParticipant(actor, body, queries)

		assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeUserNotFound)
	})

	t.Run("non-participant caller receives case not found", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.participants = nil
		response := serveAssignParticipant(actor, body, queries)

		assertCaseTypeAPIError(t, response, http.StatusNotFound, httpapi.CodeCaseNotFound)
	})

	t.Run("inactive matching row is reactivated with the same ID", func(t *testing.T) {
		queries := participantTestQueries(actor)
		existingID := handlerTestUUID(12)
		queries.participantByRole = db.CaseParticipant{
			ID: existingID, CaseID: caseID, UserID: targetUserID, Role: "CHECKER", Required: true, Status: "INACTIVE",
		}
		queries.reactivateResult = db.CaseParticipant{
			ID: existingID, CaseID: caseID, UserID: targetUserID, Role: "CHECKER", Required: true,
			Status: "ACTIVE", AssignedBy: actor.ID, AssignedAt: participantTestTime,
		}
		response := serveAssignParticipant(actor, body, queries)

		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		if queries.reactivateCalls != 1 || queries.reactivateArg.ID != existingID {
			t.Errorf("ReactivateCaseParticipant = calls %d arg %+v, want ID %v", queries.reactivateCalls, queries.reactivateArg, existingID)
		}
		if queries.createCalls != 0 {
			t.Errorf("CreateCaseParticipant calls = %d, want 0", queries.createCalls)
		}
	})

	t.Run("successful assignment defaults required and records actor", func(t *testing.T) {
		queries := participantTestQueries(actor)
		response := serveAssignParticipant(actor, body, queries)

		created := db.CaseParticipant{
			ID:         queries.createArg.ID,
			CaseID:     caseID,
			UserID:     targetUserID,
			Role:       "CHECKER",
			Required:   true,
			Status:     "ACTIVE",
			AssignedBy: actor.ID,
			AssignedAt: participantTestTime,
		}
		assertJSONResponse(t, response, http.StatusCreated, map[string]any{"data": participantJSON(created)})
		if !queries.createArg.Required || queries.createArg.AssignedBy != actor.ID {
			t.Errorf("create arg = %+v, want required=true assigned_by=%v", queries.createArg, actor.ID)
		}
		if queries.auditCalls != 1 || queries.auditArg.EventType != "PARTICIPANT_ASSIGNED" || queries.auditArg.ActorRole.String != "MAKER" {
			t.Errorf("audit arg = %+v, want PARTICIPANT_ASSIGNED by MAKER", queries.auditArg)
		}
	})
}

func TestUnassignCaseParticipant(t *testing.T) {
	actor := auth.User{ID: handlerTestUUID(9), SystemRole: auth.SystemRoleUser}
	caseID := handlerTestUUID(4)
	participantID := handlerTestUUID(11)

	t.Run("MAKER cannot be unassigned", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.activeParticipant = db.CaseParticipant{
			ID: participantID, CaseID: caseID, UserID: actor.ID, Role: "MAKER", Status: "ACTIVE",
		}
		response := serveUnassignParticipant(actor, participantID, queries)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
		if queries.unassignCalls != 0 {
			t.Errorf("UnassignCaseParticipant calls = %d, want 0", queries.unassignCalls)
		}
	})

	t.Run("non-DRAFT case returns conflict", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.caseResult.Status = "SUBMITTED"
		response := serveUnassignParticipant(actor, participantID, queries)

		assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
	})

	t.Run("successful unassignment returns participant and writes audit", func(t *testing.T) {
		queries := participantTestQueries(actor)
		queries.activeParticipant = db.CaseParticipant{
			ID: participantID, CaseID: caseID, UserID: handlerTestUUID(8), Role: "CHECKER", Required: true, Status: "ACTIVE",
		}
		queries.unassignResult = db.CaseParticipant{
			ID: participantID, CaseID: caseID, UserID: handlerTestUUID(8), Role: "CHECKER", Required: true,
			Status: "INACTIVE", AssignedBy: actor.ID, AssignedAt: participantTestTime,
			UnassignedAt: pgtype.Timestamptz{Time: participantTestTime.Add(time.Hour), Valid: true},
		}
		response := serveUnassignParticipant(actor, participantID, queries)

		assertJSONResponse(t, response, http.StatusOK, map[string]any{"data": participantJSON(queries.unassignResult)})
		if queries.unassignCalls != 1 || queries.unassignID != participantID {
			t.Errorf("unassign = calls %d ID %v, want one call with %v", queries.unassignCalls, queries.unassignID, participantID)
		}
		if queries.auditCalls != 1 || queries.auditArg.EventType != "PARTICIPANT_UNASSIGNED" {
			t.Errorf("audit arg = %+v, want PARTICIPANT_UNASSIGNED", queries.auditArg)
		}
	})
}

func serveAssignParticipant(actor auth.User, body string, queries *fakeParticipantTxQueries) *httptest.ResponseRecorder {
	store := &fakeCaseParticipantStore{queries: queries}
	request := participantRequest(http.MethodPost, body, actor, "")
	response := httptest.NewRecorder()
	handler.AssignCaseParticipant(store).ServeHTTP(response, request)
	return response
}

func serveUnassignParticipant(actor auth.User, participantID pgtype.UUID, queries *fakeParticipantTxQueries) *httptest.ResponseRecorder {
	store := &fakeCaseParticipantStore{queries: queries}
	request := participantRequest(http.MethodDelete, "", actor, participantID.String())
	response := httptest.NewRecorder()
	handler.UnassignCaseParticipant(store).ServeHTTP(response, request)
	return response
}

func participantJSON(participant db.CaseParticipant) map[string]any {
	var unassignedAt any
	if participant.UnassignedAt.Valid {
		unassignedAt = participant.UnassignedAt.Time.Format(time.RFC3339)
	}
	return map[string]any{
		"id":            participant.ID.String(),
		"case_id":       participant.CaseID.String(),
		"user_id":       participant.UserID.String(),
		"role":          participant.Role,
		"required":      participant.Required,
		"status":        participant.Status,
		"assigned_by":   participant.AssignedBy.String(),
		"assigned_at":   participant.AssignedAt.Format(time.RFC3339),
		"unassigned_at": unassignedAt,
	}
}
