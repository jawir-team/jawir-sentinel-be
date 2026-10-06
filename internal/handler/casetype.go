package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const maxCreateCaseTypeBodyBytes = 1 << 20

// CaseTypeStore is the database boundary needed by the case type collection handlers.
// *db.Queries satisfies this interface.
type CaseTypeStore interface {
	ListCaseTypes(context.Context) ([]db.CaseType, error)
	CreateCaseType(context.Context, db.CreateCaseTypeParams) (db.CaseType, error)
}

type caseTypeResponse struct {
	ID          pgtype.UUID `json:"id"`
	Code        string      `json:"code"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
}

type createCaseTypeRequest struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// ListCaseTypes returns all case types visible to an authenticated Sentinel user.
func ListCaseTypes(caseTypes CaseTypeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if caseTypes == nil {
			logging.With(r.Context()).Error("list case types: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		storedCaseTypes, err := caseTypes.ListCaseTypes(r.Context())
		if err != nil {
			logging.With(r.Context()).Error("list case types", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		response := make([]caseTypeResponse, 0, len(storedCaseTypes))
		for _, caseType := range storedCaseTypes {
			response = append(response, newCaseTypeResponse(caseType))
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// CreateCaseType creates a case type for an authenticated Sentinel administrator.
func CreateCaseType(caseTypes CaseTypeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok || user.SystemRole != auth.SystemRoleAdmin {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeForbidden, "", nil))
			return
		}
		if caseTypes == nil {
			logging.With(r.Context()).Error("create case type: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		var request createCaseTypeRequest
		if err := decodeCreateCaseTypeRequest(w, r, &request); err != nil {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Invalid request body.", nil))
			return
		}

		request.Code = strings.TrimSpace(request.Code)
		request.Name = strings.TrimSpace(request.Name)
		if request.Code == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Case type code is required.", nil))
			return
		}
		if request.Name == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "Case type name is required.", nil))
			return
		}

		id, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate case type ID", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		params := db.CreateCaseTypeParams{
			ID:   id,
			Code: request.Code,
			Name: request.Name,
		}
		if request.Description != nil {
			params.Description = pgtype.Text{String: *request.Description, Valid: true}
		}

		created, err := caseTypes.CreateCaseType(r.Context(), params)
		if err != nil {
			if isUniqueViolation(err) {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, "Case type code already exists.", nil))
				return
			}
			logging.With(r.Context()).Error("create case type", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newCaseTypeResponse(created)})
	}
}

func decodeCreateCaseTypeRequest(w http.ResponseWriter, r *http.Request, request *createCaseTypeRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateCaseTypeBodyBytes)
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

func newCaseTypeResponse(caseType db.CaseType) caseTypeResponse {
	response := caseTypeResponse{
		ID:   caseType.ID,
		Code: caseType.Code,
		Name: caseType.Name,
	}
	if caseType.Description.Valid {
		description := caseType.Description.String
		response.Description = &description
	}
	return response
}
