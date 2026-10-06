package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const (
	caseRoleChecker                = "CHECKER"
	caseRoleSigner                 = "SIGNER"
	caseRoleExecuter               = "EXECUTER"
	participantStatusActive        = "ACTIVE"
	participantStatusInactive      = "INACTIVE"
	participantEventAssigned       = "PARTICIPANT_ASSIGNED"
	participantEventUnassigned     = "PARTICIPANT_UNASSIGNED"
	participantDraftConflict       = "Only DRAFT cases can be modified."
	participantAlreadyActive       = "User already has an active role in this case."
	participantInactiveUserMessage = "Target user is not active."
)

// ParticipantTxQueries is the query boundary used by participant assignment
// transactions. *db.Queries satisfies this interface.
type ParticipantTxQueries interface {
	GetCase(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	GetUserForUpdate(context.Context, pgtype.UUID) (db.User, error)
	IsActiveCaseParticipant(context.Context, db.IsActiveCaseParticipantParams) (bool, error)
	GetParticipantByCaseUserRole(context.Context, db.GetParticipantByCaseUserRoleParams) (db.CaseParticipant, error)
	ReactivateCaseParticipant(context.Context, db.ReactivateCaseParticipantParams) (db.CaseParticipant, error)
	CreateCaseParticipant(context.Context, db.CreateCaseParticipantParams) (db.CaseParticipant, error)
	GetActiveParticipantForUpdate(context.Context, pgtype.UUID) (db.CaseParticipant, error)
	UnassignCaseParticipant(context.Context, pgtype.UUID) (db.CaseParticipant, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
}

var _ ParticipantTxQueries = (*db.Queries)(nil)

// SubmitTxQueries is the query boundary used by case submission transactions.
// *db.Queries satisfies this interface.
type SubmitTxQueries interface {
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	UpdateCaseStatus(context.Context, db.UpdateCaseStatusParams) (db.Case, error)
	CreateAnalysis(context.Context, db.CreateAnalysisParams) (db.AiAnalysis, error)
	CreateOutboxEvent(context.Context, db.CreateOutboxEventParams) (db.OutboxEvent, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
}

var _ SubmitTxQueries = (*db.Queries)(nil)

// CloseTxQueries is the query boundary used by case closure transactions.
// *db.Queries satisfies this interface.
type CloseTxQueries interface {
	GetCaseForUpdate(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	ExistsGeneratingAnalysis(context.Context, pgtype.UUID) (bool, error)
	ExistsRunningExecution(context.Context, pgtype.UUID) (bool, error)
	CloseCase(context.Context, db.CloseCaseParams) (db.Case, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
}

var _ CloseTxQueries = (*db.Queries)(nil)

// CaseParticipantStore runs participant changes atomically.
type CaseParticipantStore interface {
	RunParticipantTx(context.Context, func(context.Context, ParticipantTxQueries) error) error
}

// TxQueries combines the ordinary query store with transaction support.
type TxQueries struct {
	*db.Queries
	pool *pgxpool.Pool
}

var (
	_ CaseStore            = (*TxQueries)(nil)
	_ CaseParticipantStore = (*TxQueries)(nil)
	_ CloseCaseStore       = (*TxQueries)(nil)
	_ SubmitCaseStore      = (*TxQueries)(nil)
)

func NewTxQueries(pool *pgxpool.Pool) *TxQueries {
	return &TxQueries{Queries: db.New(pool), pool: pool}
}

func (q *TxQueries) RunParticipantTx(ctx context.Context, fn func(context.Context, ParticipantTxQueries) error) error {
	if q == nil || q.pool == nil {
		return errors.New("participant transaction store is not configured")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *TxQueries) RunSubmitTx(ctx context.Context, fn func(context.Context, SubmitTxQueries) error) error {
	if q == nil || q.pool == nil {
		return errors.New("submit transaction store is not configured")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *TxQueries) RunCloseTx(ctx context.Context, fn func(context.Context, CloseTxQueries) error) error {
	if q == nil || q.pool == nil {
		return errors.New("close transaction store is not configured")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type assignParticipantRequest struct {
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	Required *bool  `json:"required"`
}

type caseParticipantResponse struct {
	ID           pgtype.UUID `json:"id"`
	CaseID       pgtype.UUID `json:"case_id"`
	UserID       pgtype.UUID `json:"user_id"`
	Role         string      `json:"role"`
	Required     bool        `json:"required"`
	Status       string      `json:"status"`
	AssignedBy   pgtype.UUID `json:"assigned_by"`
	AssignedAt   time.Time   `json:"assigned_at"`
	UnassignedAt *time.Time  `json:"unassigned_at"`
}

// AssignCaseParticipant adds or reactivates a participant on a DRAFT case.
func AssignCaseParticipant(store CaseParticipantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("assign case participant: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request assignParticipantRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		targetUserID, err := parseUserUUID(request.UserID)
		if err != nil {
			writeInvalidCaseRequest(w, "A valid user_id is required.")
			return
		}
		role := strings.ToUpper(strings.TrimSpace(request.Role))
		if role == caseRoleMaker {
			writeInvalidCaseRequest(w, "MAKER cannot be assigned through this endpoint.")
			return
		}
		if role != caseRoleChecker && role != caseRoleSigner && role != caseRoleExecuter {
			writeInvalidCaseRequest(w, "Role must be CHECKER, SIGNER, or EXECUTER.")
			return
		}
		required := true
		if request.Required != nil {
			required = *request.Required
		}

		var participant db.CaseParticipant
		var apiErr *httpapi.APIError
		txErr := store.RunParticipantTx(r.Context(), func(ctx context.Context, q ParticipantTxQueries) error {
			participant, apiErr = runAssignParticipant(ctx, q, caseID, targetUserID, role, required, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("assign case participant", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit participant assignment", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newCaseParticipantResponse(participant)})
	}
}

// UnassignCaseParticipant marks an active non-MAKER participant inactive.
func UnassignCaseParticipant(store CaseParticipantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("unassign case participant: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		participantID, err := parseUserUUID(chi.URLParam(r, "participant_id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid participant ID is required.")
			return
		}

		var participant db.CaseParticipant
		var apiErr *httpapi.APIError
		txErr := store.RunParticipantTx(r.Context(), func(ctx context.Context, q ParticipantTxQueries) error {
			participant, apiErr = runUnassignParticipant(ctx, q, caseID, participantID, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("unassign case participant", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit participant unassignment", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newCaseParticipantResponse(participant)})
	}
}

func runAssignParticipant(ctx context.Context, q ParticipantTxQueries, caseID pgtype.UUID, targetUserID pgtype.UUID, role string, required bool, actor auth.User) (db.CaseParticipant, *httpapi.APIError) {
	storedCase, err := q.GetCase(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if storedCase.Status != caseStatusDraft {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, participantDraftConflict, nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	actorRole, allowed := participantActorRole(participants, actor)
	if !allowed {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}

	target, err := q.GetUserForUpdate(ctx, targetUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeUserNotFound, "", nil)
	}
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if target.Status != participantStatusActive {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeInvalidRequest, participantInactiveUserMessage, nil)
	}

	for _, participant := range participants {
		if participant.Status != participantStatusActive {
			continue
		}
		if participant.UserID == targetUserID {
			return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, participantAlreadyActive, nil)
		}
		if (role == caseRoleSigner || role == caseRoleExecuter) && participant.Role == role {
			return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, "An active "+role+" is already assigned to this case.", nil)
		}
	}

	existing, err := q.GetParticipantByCaseUserRole(ctx, db.GetParticipantByCaseUserRoleParams{
		CaseID: caseID,
		UserID: targetUserID,
		Role:   role,
	})
	var assigned db.CaseParticipant
	switch {
	case err == nil && existing.Status == participantStatusInactive:
		assigned, err = q.ReactivateCaseParticipant(ctx, db.ReactivateCaseParticipantParams{
			AssignedBy: actor.ID,
			ID:         existing.ID,
		})
	case err == nil:
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, participantAlreadyActive, nil)
	case errors.Is(err, pgx.ErrNoRows):
		participantID, idErr := newUnitUUID()
		if idErr != nil {
			return db.CaseParticipant{}, participantInternalError(idErr)
		}
		assigned, err = q.CreateCaseParticipant(ctx, db.CreateCaseParticipantParams{
			ID:         participantID,
			CaseID:     caseID,
			UserID:     targetUserID,
			Role:       role,
			Required:   required,
			AssignedBy: actor.ID,
		})
	default:
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, "Participant assignment conflicts with an active case role.", nil)
		}
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if apiErr := ValidateCaseSoD(append(participants, assigned)); apiErr != nil {
		return db.CaseParticipant{}, apiErr
	}
	if apiErr := appendParticipantAuditEvent(q, ctx, caseID, actor, actorRole, participantEventAssigned); apiErr != nil {
		return db.CaseParticipant{}, apiErr
	}
	return assigned, nil
}

func runUnassignParticipant(ctx context.Context, q ParticipantTxQueries, caseID, participantID pgtype.UUID, actor auth.User) (db.CaseParticipant, *httpapi.APIError) {
	storedCase, err := q.GetCase(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if storedCase.Status != caseStatusDraft {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, participantDraftConflict, nil)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	actorRole, allowed := participantActorRole(participants, actor)
	if !allowed {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}

	participant, err := q.GetActiveParticipantForUpdate(ctx, participantID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && participant.CaseID != caseID) {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if participant.Role == caseRoleMaker {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeConflict, "Maker cannot be unassigned.", nil)
	}

	unassigned, err := q.UnassignCaseParticipant(ctx, participantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CaseParticipant{}, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return db.CaseParticipant{}, participantInternalError(err)
	}
	if apiErr := appendParticipantAuditEvent(q, ctx, caseID, actor, actorRole, participantEventUnassigned); apiErr != nil {
		return db.CaseParticipant{}, apiErr
	}
	return unassigned, nil
}

func participantActorRole(participants []db.CaseParticipant, actor auth.User) (string, bool) {
	for _, participant := range participants {
		if participant.Status == participantStatusActive && participant.UserID == actor.ID {
			return participant.Role, true
		}
	}
	if actor.SystemRole == auth.SystemRoleAdmin {
		return auth.SystemRoleAdmin, true
	}
	return "", false
}

// nullableWorkflowActorRole converts an actor role to a NULL-safe pgtype.Text.
// audit_events.audit_events_actor_role_check only allows the four workflow
// roles (or NULL), so a system ADMIN acting without a case role is stored as NULL.
func nullableWorkflowActorRole(role string) pgtype.Text {
	switch role {
	case caseRoleMaker, caseRoleChecker, caseRoleSigner, caseRoleExecuter:
		return pgtype.Text{String: role, Valid: true}
	default:
		return pgtype.Text{Valid: false}
	}
}

func appendParticipantAuditEvent(q ParticipantTxQueries, ctx context.Context, caseID pgtype.UUID, actor auth.User, actorRole, eventType string) *httpapi.APIError {
	auditID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	_, err = q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:        auditID,
		CaseID:    caseID,
		EventType: eventType,
		ActorID:   actor.ID,
		ActorRole: nullableWorkflowActorRole(actorRole),
		Metadata:  []byte(`{}`),
	})
	if err != nil {
		return participantInternalError(err)
	}
	return nil
}

func participantAPIError(code httpapi.ErrorCode, message string, cause error) *httpapi.APIError {
	if cause != nil {
		return httpapi.WrapError(code, message, nil, cause)
	}
	return httpapi.NewError(code, message, nil)
}

func participantInternalError(err error) *httpapi.APIError {
	return httpapi.WrapError(httpapi.CodeInternalError, "", nil, err)
}

func newCaseParticipantResponse(participant db.CaseParticipant) caseParticipantResponse {
	response := caseParticipantResponse{
		ID:         participant.ID,
		CaseID:     participant.CaseID,
		UserID:     participant.UserID,
		Role:       participant.Role,
		Required:   participant.Required,
		Status:     participant.Status,
		AssignedBy: participant.AssignedBy,
		AssignedAt: participant.AssignedAt,
	}
	if participant.UnassignedAt.Valid {
		unassignedAt := participant.UnassignedAt.Time
		response.UnassignedAt = &unassignedAt
	}
	return response
}
