package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const (
	maxCaseBodyBytes = 1 << 20
	defaultUrgency   = "MEDIUM"
	caseStatusDraft  = "DRAFT"
	caseRoleMaker    = "MAKER"
	caseEventCreated = "CASE_CREATED"
)

// CaseStore is the database boundary needed by the case CRUD handlers.
// *db.Queries satisfies this interface.
type CaseStore interface {
	CreateCase(context.Context, db.CreateCaseParams) (db.Case, error)
	GetCase(context.Context, pgtype.UUID) (db.Case, error)
	ListCasesForUser(context.Context, db.ListCasesForUserParams) ([]db.Case, error)
	UpdateCase(context.Context, db.UpdateCaseParams) (db.Case, error)
	CreateCaseParticipant(context.Context, db.CreateCaseParticipantParams) (db.CaseParticipant, error)
	IsActiveCaseParticipant(context.Context, db.IsActiveCaseParticipantParams) (bool, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
}

var _ CaseStore = (*db.Queries)(nil)

type caseResponse struct {
	ID                pgtype.UUID  `json:"id"`
	CaseNumber        string       `json:"case_number"`
	CaseTypeID        pgtype.UUID  `json:"case_type_id"`
	Title             string       `json:"title"`
	Description       string       `json:"description"`
	Urgency           string       `json:"urgency"`
	Status            string       `json:"status"`
	CreatedBy         pgtype.UUID  `json:"created_by"`
	OwnerID           pgtype.UUID  `json:"owner_id"`
	CurrentAnalysisID *pgtype.UUID `json:"current_analysis_id"`
	ClosedBy          *pgtype.UUID `json:"closed_by"`
	CloseReason       *string      `json:"close_reason"`
	ClosedAt          *time.Time   `json:"closed_at"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

type createCaseRequest struct {
	CaseTypeID  string  `json:"case_type_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Urgency     *string `json:"urgency"`
}

type updateCaseRequest struct {
	CaseTypeID  *string `json:"case_type_id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Urgency     *string `json:"urgency"`
}

// CreateCase creates a DRAFT case owned by the caller, assigns that caller as
// its required MAKER, and records the creation event.
func CreateCase(cases CaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if cases == nil {
			logging.With(r.Context()).Error("create case: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		var request createCaseRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		caseTypeID, err := parseUserUUID(request.CaseTypeID)
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case_type_id is required.")
			return
		}
		title := strings.TrimSpace(request.Title)
		if title == "" {
			writeInvalidCaseRequest(w, "Case title is required.")
			return
		}
		description := ""
		if request.Description != nil {
			description = strings.TrimSpace(*request.Description)
		}
		urgency := defaultUrgency
		if request.Urgency != nil {
			urgency = strings.ToUpper(strings.TrimSpace(*request.Urgency))
		}
		if !validCaseUrgency(urgency) {
			writeInvalidCaseRequest(w, "Urgency must be LOW, MEDIUM, HIGH, or CRITICAL.")
			return
		}

		caseID, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate case ID", "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		created, err := cases.CreateCase(r.Context(), db.CreateCaseParams{
			ID:          caseID,
			CaseNumber:  "CASE-" + strings.ToUpper(caseID.String()),
			CaseTypeID:  caseTypeID,
			Title:       title,
			Description: description,
			Urgency:     urgency,
			CreatedBy:   user.ID,
			OwnerID:     user.ID,
		})
		if err != nil {
			if isForeignKeyViolation(err) {
				writeInvalidCaseRequest(w, "Case type does not exist.")
				return
			}
			logging.With(r.Context()).Error("create case", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		participantID, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate maker participant ID", "case_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		if _, err := cases.CreateCaseParticipant(r.Context(), db.CreateCaseParticipantParams{
			ID:         participantID,
			CaseID:     created.ID,
			UserID:     user.ID,
			Role:       caseRoleMaker,
			Required:   true,
			AssignedBy: user.ID,
		}); err != nil {
			logging.With(r.Context()).Error("create maker participant", "case_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		auditID, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate case audit event ID", "case_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		if _, err := cases.AppendCaseAuditEvent(r.Context(), db.AppendCaseAuditEventParams{
			ID:        auditID,
			CaseID:    created.ID,
			EventType: caseEventCreated,
			ActorID:   user.ID,
			ActorRole: pgtype.Text{String: caseRoleMaker, Valid: true},
			Metadata:  []byte(`{}`),
		}); err != nil {
			logging.With(r.Context()).Error("append case creation audit event", "case_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newCaseResponse(created)})
	}
}

// ListCases returns all cases to administrators and active-participant cases
// to regular users.
func ListCases(cases CaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if cases == nil {
			logging.With(r.Context()).Error("list cases: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		storedCases, err := cases.ListCasesForUser(r.Context(), db.ListCasesForUserParams{
			IsAdmin: user.SystemRole == auth.SystemRoleAdmin,
			UserID:  user.ID,
		})
		if err != nil {
			logging.With(r.Context()).Error("list cases", "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		response := make([]caseResponse, 0, len(storedCases))
		for _, stored := range storedCases {
			response = append(response, newCaseResponse(stored))
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// GetCase returns one case when the caller is an active participant or an
// administrator. An inaccessible case is deliberately indistinguishable from
// a missing case.
func GetCase(cases CaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if cases == nil {
			logging.With(r.Context()).Error("get case: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		if allowed, err := canReadCase(r.Context(), cases, user, caseID); err != nil {
			logging.With(r.Context()).Error("check case access", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		} else if !allowed {
			writeCaseNotFound(w)
			return
		}

		stored, err := cases.GetCase(r.Context(), caseID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeCaseNotFound(w)
				return
			}
			logging.With(r.Context()).Error("get case", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newCaseResponse(stored)})
	}
}

// UpdateCase updates editable fields while the case remains in DRAFT.
func UpdateCase(cases CaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if cases == nil {
			logging.With(r.Context()).Error("update case: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request updateCaseRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		if request.CaseTypeID == nil && request.Title == nil && request.Description == nil && request.Urgency == nil {
			writeInvalidCaseRequest(w, "At least one editable case field must be provided.")
			return
		}

		if allowed, err := canReadCase(r.Context(), cases, user, caseID); err != nil {
			logging.With(r.Context()).Error("check case access for update", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		} else if !allowed {
			writeCaseNotFound(w)
			return
		}
		stored, err := cases.GetCase(r.Context(), caseID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeCaseNotFound(w)
				return
			}
			logging.With(r.Context()).Error("get case for update", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		if stored.Status != caseStatusDraft {
			writeCaseUpdateConflict(w)
			return
		}

		params := db.UpdateCaseParams{
			CaseTypeID:  stored.CaseTypeID,
			Title:       stored.Title,
			Description: stored.Description,
			Urgency:     stored.Urgency,
			ID:          stored.ID,
		}
		if request.CaseTypeID != nil {
			params.CaseTypeID, err = parseUserUUID(*request.CaseTypeID)
			if err != nil {
				writeInvalidCaseRequest(w, "case_type_id must be a valid UUID.")
				return
			}
		}
		if request.Title != nil {
			params.Title = strings.TrimSpace(*request.Title)
			if params.Title == "" {
				writeInvalidCaseRequest(w, "Case title cannot be empty.")
				return
			}
		}
		if request.Description != nil {
			params.Description = strings.TrimSpace(*request.Description)
		}
		if request.Urgency != nil {
			params.Urgency = strings.ToUpper(strings.TrimSpace(*request.Urgency))
			if !validCaseUrgency(params.Urgency) {
				writeInvalidCaseRequest(w, "Urgency must be LOW, MEDIUM, HIGH, or CRITICAL.")
				return
			}
		}

		updated, err := cases.UpdateCase(r.Context(), params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeCaseUpdateConflict(w)
				return
			}
			if isForeignKeyViolation(err) {
				writeInvalidCaseRequest(w, "Case type does not exist.")
				return
			}
			logging.With(r.Context()).Error("update case", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newCaseResponse(updated)})
	}
}

func decodeCaseRequest(w http.ResponseWriter, r *http.Request, request any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCaseBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func canReadCase(ctx context.Context, cases CaseStore, user auth.User, caseID pgtype.UUID) (bool, error) {
	if user.SystemRole == auth.SystemRoleAdmin {
		return true, nil
	}
	return cases.IsActiveCaseParticipant(ctx, db.IsActiveCaseParticipantParams{
		CaseID: caseID,
		UserID: user.ID,
	})
}

func validCaseUrgency(urgency string) bool {
	switch urgency {
	case "LOW", "MEDIUM", "HIGH", "CRITICAL":
		return true
	default:
		return false
	}
}

func writeInvalidCaseRequest(w http.ResponseWriter, message string) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, message, nil))
}

func writeCaseNotFound(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeCaseNotFound, "", nil))
}

func writeCaseUpdateConflict(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, "Only DRAFT cases can be updated.", nil))
}

func newCaseResponse(stored db.Case) caseResponse {
	response := caseResponse{
		ID:          stored.ID,
		CaseNumber:  stored.CaseNumber,
		CaseTypeID:  stored.CaseTypeID,
		Title:       stored.Title,
		Description: stored.Description,
		Urgency:     stored.Urgency,
		Status:      stored.Status,
		CreatedBy:   stored.CreatedBy,
		OwnerID:     stored.OwnerID,
		CreatedAt:   stored.CreatedAt,
		UpdatedAt:   stored.UpdatedAt,
	}
	if stored.CurrentAnalysisID.Valid {
		id := stored.CurrentAnalysisID
		response.CurrentAnalysisID = &id
	}
	if stored.ClosedBy.Valid {
		id := stored.ClosedBy
		response.ClosedBy = &id
	}
	if stored.CloseReason.Valid {
		reason := stored.CloseReason.String
		response.CloseReason = &reason
	}
	if stored.ClosedAt.Valid {
		closedAt := stored.ClosedAt.Time
		response.ClosedAt = &closedAt
	}
	return response
}
