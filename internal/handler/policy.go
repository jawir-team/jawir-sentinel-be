package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/audit"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const (
	maxCreatePolicyBodyBytes = 1 << 20
	policyEventCreated       = "POLICY_CREATED"
)

// PolicyStore is the database boundary needed by the policy collection handlers.
type PolicyStore interface {
	audit.Queries
	ListPolicies(context.Context) ([]db.Policy, error)
	CreatePolicy(context.Context, db.CreatePolicyParams) (db.Policy, error)
}

var _ PolicyStore = (*db.Queries)(nil)

type policyResponse struct {
	ID          pgtype.UUID  `json:"id"`
	Code        string       `json:"code"`
	Title       string       `json:"title"`
	Domain      string       `json:"domain"`
	CaseTypeID  *pgtype.UUID `json:"case_type_id"`
	Description *string      `json:"description"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type createPolicyRequest struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Domain      string `json:"domain"`
	CaseTypeID  string `json:"case_type_id"`
	Description string `json:"description"`
}

// ListPolicies returns every policy to an authenticated Sentinel user.
func ListPolicies(store PolicyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("list policies: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		storedPolicies, err := store.ListPolicies(r.Context())
		if err != nil {
			logging.With(r.Context()).Error("list policies", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		response := make([]policyResponse, 0, len(storedPolicies))
		for _, policy := range storedPolicies {
			response = append(response, newPolicyResponse(policy))
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// CreatePolicy creates a policy and records its creation audit event. The
// route's RequireAdmin middleware owns authorization for this handler.
func CreatePolicy(store PolicyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("create policy: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		var request createPolicyRequest
		if err := decodeCreatePolicyRequest(w, r, &request); err != nil {
			writeInvalidPolicyRequest(w, "Invalid request body.")
			return
		}

		request.Code = strings.TrimSpace(request.Code)
		request.Title = strings.TrimSpace(request.Title)
		request.Domain = strings.TrimSpace(request.Domain)
		if request.Code == "" {
			writeInvalidPolicyRequest(w, "Policy code is required.")
			return
		}
		if request.Title == "" {
			writeInvalidPolicyRequest(w, "Policy title is required.")
			return
		}
		if request.Domain == "" {
			writeInvalidPolicyRequest(w, "Policy domain is required.")
			return
		}

		params := db.CreatePolicyParams{
			Code:   request.Code,
			Title:  request.Title,
			Domain: request.Domain,
		}
		if value := strings.TrimSpace(request.CaseTypeID); value != "" {
			caseTypeID, err := parseUserUUID(value)
			if err != nil {
				writeInvalidPolicyRequest(w, "case_type_id must be a valid UUID.")
				return
			}
			params.CaseTypeID = caseTypeID
		}
		if description := strings.TrimSpace(request.Description); description != "" {
			params.Description = pgtype.Text{String: description, Valid: true}
		}

		var err error
		params.ID, err = newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate policy ID", "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		created, err := store.CreatePolicy(r.Context(), params)
		if err != nil {
			switch {
			case isUniqueViolation(err):
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, "Policy code already exists.", nil))
			case isForeignKeyViolation(err):
				writeInvalidPolicyRequest(w, "Unknown case_type_id.")
			default:
				logging.With(r.Context()).Error("create policy", "error", err)
				httpapi.WriteError(w, nil)
			}
			return
		}

		if _, err := audit.AppendPolicyEvent(r.Context(), store, audit.PolicyEvent{
			PolicyID:  created.ID,
			EventType: policyEventCreated,
			ActorID:   actor.ID,
			Metadata:  []byte(`{}`),
		}); err != nil {
			logging.With(r.Context()).Error("append policy creation audit event", "policy_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newPolicyResponse(created)})
	}
}

func decodeCreatePolicyRequest(w http.ResponseWriter, r *http.Request, request *createPolicyRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreatePolicyBodyBytes)
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

func writeInvalidPolicyRequest(w http.ResponseWriter, message string) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, message, nil))
}

func newPolicyResponse(policy db.Policy) policyResponse {
	response := policyResponse{
		ID:        policy.ID,
		Code:      policy.Code,
		Title:     policy.Title,
		Domain:    policy.Domain,
		CreatedAt: policy.CreatedAt,
		UpdatedAt: policy.UpdatedAt,
	}
	if policy.CaseTypeID.Valid {
		caseTypeID := policy.CaseTypeID
		response.CaseTypeID = &caseTypeID
	}
	if policy.Description.Valid {
		description := policy.Description.String
		response.Description = &description
	}
	return response
}
