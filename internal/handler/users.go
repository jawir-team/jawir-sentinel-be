package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const (
	defaultUserListLimit = 20
	maxUserListLimit     = 100
	maxUserBodyBytes     = 1 << 20

	userStatusActive   = "ACTIVE"
	userStatusInactive = "INACTIVE"
)

// UserStore is the database boundary needed by the user directory handlers.
// *db.Queries satisfies this interface.
type UserStore interface {
	ListUsers(context.Context, db.ListUsersParams) ([]db.ListUsersRow, error)
	CreateUser(context.Context, db.CreateUserParams) (db.User, error)
	GetUser(context.Context, pgtype.UUID) (db.User, error)
	CountActiveAdmins(context.Context) (int64, error)
	UpdateUser(context.Context, db.UpdateUserParams) (db.User, error)
}

// userResponse is deliberately allow-listed. In particular, FirebaseUID must
// never be included in directory or mutation responses.
type userResponse struct {
	ID         pgtype.UUID `json:"id"`
	UnitID     pgtype.UUID `json:"unit_id"`
	Name       string      `json:"name"`
	Email      string      `json:"email"`
	Status     string      `json:"status"`
	SystemRole string      `json:"system_role"`
}

type createUserRequest struct {
	FirebaseUID string `json:"firebase_uid"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	UnitID      string `json:"unit_id"`
	Status      string `json:"status"`
	SystemRole  string `json:"system_role"`
}

type updateUserRequest struct {
	Name       *string `json:"name"`
	Email      *string `json:"email"`
	UnitID     *string `json:"unit_id"`
	Status     *string `json:"status"`
	SystemRole *string `json:"system_role"`
}

// ListUsers returns the safe user directory to any authenticated Sentinel
// user. The optional status filter is independent of the caller's ACTIVE
// status, which is enforced by authentication middleware.
func ListUsers(users UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if users == nil {
			logging.With(r.Context()).Error("list users: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		params, err := listUsersParams(r)
		if err != nil {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, err.Error(), nil))
			return
		}

		storedUsers, err := users.ListUsers(r.Context(), params)
		if err != nil {
			logging.With(r.Context()).Error("list users", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		response := make([]userResponse, 0, len(storedUsers))
		for _, user := range storedUsers {
			response = append(response, userResponse{
				ID:         user.ID,
				UnitID:     user.UnitID,
				Name:       user.Name,
				Email:      user.Email,
				Status:     user.Status,
				SystemRole: user.SystemRole,
			})
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// CreateUser creates a Sentinel user. The Firebase identity is accepted as an
// input but is intentionally omitted from the response.
func CreateUser(users UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requestUserIsAdmin(r) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeForbidden, "", nil))
			return
		}
		if users == nil {
			logging.With(r.Context()).Error("create user: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		var request createUserRequest
		if err := decodeUserRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Invalid request body.", nil))
			return
		}

		request.FirebaseUID = strings.TrimSpace(request.FirebaseUID)
		request.Name = strings.TrimSpace(request.Name)
		request.Email = strings.TrimSpace(request.Email)
		request.Status = strings.ToUpper(strings.TrimSpace(request.Status))
		request.SystemRole = strings.ToUpper(strings.TrimSpace(request.SystemRole))
		if request.FirebaseUID == "" {
			writeInvalidUserRequest(w, "Firebase UID is required.")
			return
		}
		if request.Name == "" {
			writeInvalidUserRequest(w, "User name is required.")
			return
		}
		if request.Email == "" {
			writeInvalidUserRequest(w, "Email is required.")
			return
		}
		unitID, err := parseUserUUID(request.UnitID)
		if err != nil {
			writeInvalidUserRequest(w, "A valid unit_id is required.")
			return
		}
		if !validUserStatus(request.Status) {
			writeInvalidUserRequest(w, "Status must be ACTIVE or INACTIVE.")
			return
		}
		if !validSystemRole(request.SystemRole) {
			writeInvalidUserRequest(w, "System role must be USER or ADMIN.")
			return
		}

		id, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate user ID", "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		created, err := users.CreateUser(r.Context(), db.CreateUserParams{
			ID:          id,
			UnitID:      unitID,
			FirebaseUID: request.FirebaseUID,
			Name:        request.Name,
			Email:       request.Email,
			Status:      request.Status,
			SystemRole:  request.SystemRole,
		})
		if err != nil {
			if message, ok := userUniqueConflict(err); ok {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, message, nil))
				return
			}
			if isForeignKeyViolation(err) {
				writeInvalidUserRequest(w, "Unit does not exist.")
				return
			}
			logging.With(r.Context()).Error("create user", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newUserResponse(created)})
	}
}

// UpdateUser applies the supplied user fields. Historical users are retained;
// callers deactivate them by setting status to INACTIVE rather than deleting.
func UpdateUser(users UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requestUserIsAdmin(r) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeForbidden, "", nil))
			return
		}
		if users == nil {
			logging.With(r.Context()).Error("update user: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		id, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidUserRequest(w, "A valid user ID is required.")
			return
		}

		var request updateUserRequest
		if err := decodeUserRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Invalid request body.", nil))
			return
		}
		if request.Name == nil && request.Email == nil && request.UnitID == nil && request.Status == nil && request.SystemRole == nil {
			writeInvalidUserRequest(w, "At least one user field must be provided.")
			return
		}

		stored, err := users.GetUser(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUserNotFound, "", nil))
				return
			}
			logging.With(r.Context()).Error("get user for update", "user_id", id.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		params := db.UpdateUserParams{
			ID:         stored.ID,
			UnitID:     stored.UnitID,
			Name:       stored.Name,
			Email:      stored.Email,
			Status:     stored.Status,
			SystemRole: stored.SystemRole,
		}
		if request.Name != nil {
			params.Name = strings.TrimSpace(*request.Name)
			if params.Name == "" {
				writeInvalidUserRequest(w, "User name cannot be empty.")
				return
			}
		}
		if request.Email != nil {
			params.Email = strings.TrimSpace(*request.Email)
			if params.Email == "" {
				writeInvalidUserRequest(w, "Email cannot be empty.")
				return
			}
		}
		if request.UnitID != nil {
			params.UnitID, err = parseUserUUID(*request.UnitID)
			if err != nil {
				writeInvalidUserRequest(w, "unit_id must be a valid UUID.")
				return
			}
		}
		if request.Status != nil {
			params.Status = strings.ToUpper(strings.TrimSpace(*request.Status))
			if !validUserStatus(params.Status) {
				writeInvalidUserRequest(w, "Status must be ACTIVE or INACTIVE.")
				return
			}
		}
		if request.SystemRole != nil {
			params.SystemRole = strings.ToUpper(strings.TrimSpace(*request.SystemRole))
			if !validSystemRole(params.SystemRole) {
				writeInvalidUserRequest(w, "System role must be USER or ADMIN.")
				return
			}
		}

		demotesActiveAdmin := stored.Status == userStatusActive &&
			stored.SystemRole == auth.SystemRoleAdmin &&
			(params.Status != userStatusActive || params.SystemRole != auth.SystemRoleAdmin)
		if demotesActiveAdmin {
			activeAdmins, err := users.CountActiveAdmins(r.Context())
			if err != nil {
				logging.With(r.Context()).Error("count active admins", "user_id", id.String(), "error", err)
				httpapi.WriteError(w, nil)
				return
			}
			if activeAdmins <= 1 {
				writeLastActiveAdminConflict(w)
				return
			}
		}

		updated, err := users.UpdateUser(r.Context(), params)
		if err != nil {
			if message, ok := userUniqueConflict(err); ok {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, message, nil))
				return
			}
			if isForeignKeyViolation(err) {
				writeInvalidUserRequest(w, "Unit does not exist.")
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if demotesActiveAdmin {
					// UpdateUser also enforces the invariant so a concurrent
					// administrator change cannot bypass the check above.
					writeLastActiveAdminConflict(w)
					return
				}
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUserNotFound, "", nil))
				return
			}
			logging.With(r.Context()).Error("update user", "user_id", id.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newUserResponse(updated)})
	}
}

func listUsersParams(r *http.Request) (db.ListUsersParams, error) {
	params := db.ListUsersParams{
		Search: strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  defaultUserListLimit,
		Offset: 0,
	}

	if rawStatus := strings.TrimSpace(r.URL.Query().Get("status")); rawStatus != "" {
		params.Status = strings.ToUpper(rawStatus)
		if !validUserStatus(params.Status) {
			return db.ListUsersParams{}, errors.New("status must be ACTIVE or INACTIVE")
		}
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > maxUserListLimit {
			return db.ListUsersParams{}, errors.New("limit must be an integer between 1 and 100")
		}
		params.Limit = int32(limit)
	}
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil || offset < 0 || int64(offset) > int64(^uint32(0)>>1) {
			return db.ListUsersParams{}, errors.New("offset must be a non-negative 32-bit integer")
		}
		params.Offset = int32(offset)
	}
	return params, nil
}

func decodeUserRequest(w http.ResponseWriter, r *http.Request, request any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUserBodyBytes)
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

func parseUserUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(value)); err != nil || !id.Valid {
		if err == nil {
			err = errors.New("UUID is required")
		}
		return pgtype.UUID{}, err
	}
	return id, nil
}

func validUserStatus(status string) bool {
	return status == userStatusActive || status == userStatusInactive
}

func validSystemRole(role string) bool {
	return role == auth.SystemRoleUser || role == auth.SystemRoleAdmin
}

func requestUserIsAdmin(r *http.Request) bool {
	user, ok := auth.FromContext(r.Context())
	return ok && user.SystemRole == auth.SystemRoleAdmin
}

func writeInvalidUserRequest(w http.ResponseWriter, message string) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, message, nil))
}

func writeLastActiveAdminConflict(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.NewError(
		httpapi.CodeConflict,
		"The last active administrator cannot be demoted or deactivated.",
		nil,
	))
}

func newUserResponse(user db.User) userResponse {
	return userResponse{
		ID:         user.ID,
		UnitID:     user.UnitID,
		Name:       user.Name,
		Email:      user.Email,
		Status:     user.Status,
		SystemRole: user.SystemRole,
	}
}

func userUniqueConflict(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return "", false
	}
	switch pgErr.ConstraintName {
	case "users_email_key":
		return "Email already exists.", true
	case "users_firebase_uid_key":
		return "Firebase UID already exists.", true
	default:
		return "A user with these details already exists.", true
	}
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
