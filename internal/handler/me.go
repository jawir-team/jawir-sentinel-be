// Package handler contains HTTP handlers for the Sentinel API.
package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

// MeUnitStore is the database boundary needed by GetMe. *db.Queries satisfies
// this interface.
type MeUnitStore interface {
	GetUnit(context.Context, pgtype.UUID) (db.Unit, error)
}

type meResponse struct {
	ID         pgtype.UUID     `json:"id"`
	Name       string          `json:"name"`
	Email      string          `json:"email"`
	SystemRole string          `json:"system_role"`
	Unit       *meUnitResponse `json:"unit"`
}

type meUnitResponse struct {
	ID   pgtype.UUID `json:"id"`
	Code string      `json:"code"`
	Name string      `json:"name"`
}

// GetMe returns the authenticated Sentinel user and their unit. Authentication
// middleware normally guarantees a valid unit ID; the nil unit response is a
// defensive fallback for contexts created outside that middleware.
func GetMe(units MeUnitStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}

		response := meResponse{
			ID:         user.ID,
			Name:       user.Name,
			Email:      user.Email,
			SystemRole: user.SystemRole,
		}
		if !user.UnitID.Valid {
			httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
			return
		}

		if units == nil {
			logging.With(r.Context()).Error("get current user unit: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		unit, err := units.GetUnit(r.Context(), user.UnitID)
		if err != nil {
			logging.With(r.Context()).Error(
				"get current user unit",
				"unit_id", user.UnitID.String(),
			)
			httpapi.WriteError(w, nil)
			return
		}

		response.Unit = &meUnitResponse{
			ID:   unit.ID,
			Code: unit.Code,
			Name: unit.Name,
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}
