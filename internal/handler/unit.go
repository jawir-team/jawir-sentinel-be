package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const maxCreateUnitBodyBytes = 1 << 20

// UnitStore is the database boundary needed by the unit collection handlers.
// *db.Queries satisfies this interface.
type UnitStore interface {
	ListUnits(context.Context) ([]db.Unit, error)
	CreateUnit(context.Context, db.CreateUnitParams) (db.Unit, error)
}

type unitResponse struct {
	ID          pgtype.UUID `json:"id"`
	Code        string      `json:"code"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
}

type createUnitRequest struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// ListUnits returns all units visible to an authenticated Sentinel user.
func ListUnits(units UnitStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if units == nil {
			logging.With(r.Context()).Error("list units: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		storedUnits, err := units.ListUnits(r.Context())
		if err != nil {
			logging.With(r.Context()).Error("list units", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		response := make([]unitResponse, 0, len(storedUnits))
		for _, unit := range storedUnits {
			response = append(response, newUnitResponse(unit))
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// CreateUnit creates a unit for an authenticated Sentinel administrator.
func CreateUnit(units UnitStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok || user.SystemRole != auth.SystemRoleAdmin {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeForbidden, "", nil))
			return
		}
		if units == nil {
			logging.With(r.Context()).Error("create unit: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		var request createUnitRequest
		if err := decodeCreateUnitRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Invalid request body.", nil))
			return
		}

		request.Code = strings.TrimSpace(request.Code)
		request.Name = strings.TrimSpace(request.Name)
		if request.Code == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Unit code is required.", nil))
			return
		}
		if request.Name == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Unit name is required.", nil))
			return
		}

		id, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate unit ID", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		params := db.CreateUnitParams{
			ID:   id,
			Code: request.Code,
			Name: request.Name,
		}
		if request.Description != nil {
			params.Description = pgtype.Text{String: *request.Description, Valid: true}
		}

		created, err := units.CreateUnit(r.Context(), params)
		if err != nil {
			if isUniqueViolation(err) {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, "Unit code already exists.", nil))
				return
			}
			logging.With(r.Context()).Error("create unit", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newUnitResponse(created)})
	}
}

func decodeCreateUnitRequest(w http.ResponseWriter, r *http.Request, request *createUnitRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateUnitBodyBytes)
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

func newUnitUUID() (pgtype.UUID, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return pgtype.UUID{}, err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func newUnitResponse(unit db.Unit) unitResponse {
	response := unitResponse{
		ID:   unit.ID,
		Code: unit.Code,
		Name: unit.Name,
	}
	if unit.Description.Valid {
		description := unit.Description.String
		response.Description = &description
	}
	return response
}
