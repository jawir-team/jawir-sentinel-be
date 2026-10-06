package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
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
	maxCreatePolicyVersionBodyBytes = 1 << 20
	policyVersionEventCreated       = "POLICY_VERSION_CREATED"
	defaultPolicyIndexLeaseSeconds  = int64(900)
)

// PolicyVersionStore is the database boundary needed by policy version
// collection and detail handlers.
type PolicyVersionStore interface {
	GetPolicy(context.Context, pgtype.UUID) (db.Policy, error)
	GetPolicyVersion(context.Context, pgtype.UUID) (db.PolicyVersion, error)
	ListPolicyVersions(context.Context, pgtype.UUID) ([]db.PolicyVersion, error)
	CreatePolicyVersion(context.Context, db.CreatePolicyVersionParams) (db.PolicyVersion, error)
	AppendPolicyAuditEvent(context.Context, db.AppendPolicyAuditEventParams) (db.AuditEvent, error)
}

var _ PolicyVersionStore = (*db.Queries)(nil)

type policyVersionResponse struct {
	ID               pgtype.UUID  `json:"id"`
	PolicyID         pgtype.UUID  `json:"policy_id"`
	Version          string       `json:"version"`
	Status           string       `json:"status"`
	Content          string       `json:"content"`
	FilePath         *string      `json:"file_path"`
	EffectiveFrom    *time.Time   `json:"effective_from"`
	EffectiveUntil   *time.Time   `json:"effective_until"`
	IndexStatus      string       `json:"index_status"`
	IndexError       *string      `json:"index_error"`
	IndexAttemptID   *pgtype.UUID `json:"index_attempt_id"`
	IndexStartedAt   *time.Time   `json:"index_started_at"`
	IndexedAt        *time.Time   `json:"indexed_at"`
	CreatedBy        pgtype.UUID  `json:"created_by"`
	CreatedAt        time.Time    `json:"created_at"`
	IndexRecoverable bool         `json:"index_recoverable"`
}

type createPolicyVersionRequest struct {
	Version        string `json:"version"`
	Content        string `json:"content"`
	FilePath       string `json:"file_path"`
	EffectiveFrom  string `json:"effective_from"`
	EffectiveUntil string `json:"effective_until"`
}

// CreatePolicyVersion creates an immutable DRAFT version. The route's
// RequireAdmin middleware owns authorization for this handler.
func CreatePolicyVersion(store PolicyVersionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("create policy version: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		policyID, ok := policyVersionParentID(w, r, store)
		if !ok {
			return
		}

		var request createPolicyVersionRequest
		if err := decodeCreatePolicyVersionRequest(w, r, &request); err != nil {
			writeInvalidPolicyVersionRequest(w, "Invalid request body.")
			return
		}
		request.Version = strings.TrimSpace(request.Version)
		if request.Version == "" {
			writeInvalidPolicyVersionRequest(w, "Policy version is required.")
			return
		}
		if strings.TrimSpace(request.Content) == "" {
			writeInvalidPolicyVersionRequest(w, "Policy content is required.")
			return
		}

		params := db.CreatePolicyVersionParams{
			PolicyID:  policyID,
			Version:   request.Version,
			Content:   request.Content,
			CreatedBy: actor.ID,
		}
		if filePath := strings.TrimSpace(request.FilePath); filePath != "" {
			params.FilePath = pgtype.Text{String: filePath, Valid: true}
		}

		var err error
		params.EffectiveFrom, err = parseOptionalPolicyVersionTime(request.EffectiveFrom)
		if err != nil {
			writeInvalidPolicyVersionRequest(w, "effective_from must be an RFC3339 timestamp.")
			return
		}
		params.EffectiveUntil, err = parseOptionalPolicyVersionTime(request.EffectiveUntil)
		if err != nil {
			writeInvalidPolicyVersionRequest(w, "effective_until must be an RFC3339 timestamp.")
			return
		}
		if params.EffectiveFrom.Valid && params.EffectiveUntil.Valid &&
			!params.EffectiveUntil.Time.After(params.EffectiveFrom.Time) {
			writeInvalidPolicyVersionRequest(w, "effective_until must be after effective_from")
			return
		}

		params.ID, err = newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate policy version ID", "policy_id", policyID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		created, err := store.CreatePolicyVersion(r.Context(), params)
		if err != nil {
			if isUniqueViolation(err) {
				httpapi.WriteError(w, httpapi.NewError(httpapi.CodeConflict, "Policy version already exists.", nil))
				return
			}
			logging.With(r.Context()).Error("create policy version", "policy_id", policyID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		auditID, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate policy version audit event ID", "policy_id", policyID.String(), "policy_version_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		if _, err := store.AppendPolicyAuditEvent(r.Context(), db.AppendPolicyAuditEventParams{
			ID:              auditID,
			PolicyID:        policyID,
			PolicyVersionID: created.ID,
			EventType:       policyVersionEventCreated,
			ActorID:         actor.ID,
			Metadata:        []byte(`{}`),
		}); err != nil {
			logging.With(r.Context()).Error("append policy version creation audit event", "policy_id", policyID.String(), "policy_version_id", created.ID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: newPolicyVersionResponse(created)})
	}
}

// ListPolicyVersions returns all immutable versions of a policy in the order
// supplied by the database (created_at, then id).
func ListPolicyVersions(store PolicyVersionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("list policy versions: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		policyID, ok := policyVersionParentID(w, r, store)
		if !ok {
			return
		}

		versions, err := store.ListPolicyVersions(r.Context(), policyID)
		if err != nil {
			logging.With(r.Context()).Error("list policy versions", "policy_id", policyID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		response := make([]policyVersionResponse, 0, len(versions))
		for _, version := range versions {
			response = append(response, newPolicyVersionResponse(version))
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

// GetPolicyVersion returns one immutable version. A version belonging to a
// different policy is deliberately indistinguishable from a missing version.
func GetPolicyVersion(store PolicyVersionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil {
			logging.With(r.Context()).Error("get policy version: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		policyID, ok := policyVersionParentID(w, r, store)
		if !ok {
			return
		}
		versionID, err := parseUserUUID(chi.URLParam(r, "version_id"))
		if err != nil {
			writeInvalidPolicyVersionRequest(w, "version_id must be a valid UUID.")
			return
		}

		version, err := store.GetPolicyVersion(r.Context(), versionID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && version.PolicyID != policyID) {
			writePolicyVersionNotFound(w)
			return
		}
		if err != nil {
			logging.With(r.Context()).Error("get policy version", "policy_id", policyID.String(), "policy_version_id", versionID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newPolicyVersionResponse(version)})
	}
}

// Policy versions intentionally have no PUT, PATCH, or DELETE handlers.
// Their content is immutable; changes must be represented by a new version.

func policyVersionParentID(w http.ResponseWriter, r *http.Request, store PolicyVersionStore) (pgtype.UUID, bool) {
	policyID, err := parseUserUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeInvalidPolicyVersionRequest(w, "id must be a valid policy UUID.")
		return pgtype.UUID{}, false
	}
	if _, err := store.GetPolicy(r.Context(), policyID); errors.Is(err, pgx.ErrNoRows) {
		writePolicyVersionNotFound(w)
		return pgtype.UUID{}, false
	} else if err != nil {
		logging.With(r.Context()).Error("get policy for version request", "policy_id", policyID.String(), "error", err)
		httpapi.WriteError(w, nil)
		return pgtype.UUID{}, false
	}
	return policyID, true
}

func decodeCreatePolicyVersionRequest(w http.ResponseWriter, r *http.Request, request *createPolicyVersionRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreatePolicyVersionBodyBytes)
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

func parseOptionalPolicyVersionTime(value string) (pgtype.Timestamptz, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Timestamptz{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, nil
}

func newPolicyVersionResponse(version db.PolicyVersion) policyVersionResponse {
	response := policyVersionResponse{
		ID:               version.ID,
		PolicyID:         version.PolicyID,
		Version:          version.Version,
		Status:           version.Status,
		Content:          version.Content,
		IndexStatus:      version.IndexStatus,
		CreatedBy:        version.CreatedBy,
		CreatedAt:        version.CreatedAt,
		IndexRecoverable: policyVersionIndexRecoverable(version, time.Now()),
	}
	if version.FilePath.Valid {
		response.FilePath = &version.FilePath.String
	}
	if version.EffectiveFrom.Valid {
		response.EffectiveFrom = &version.EffectiveFrom.Time
	}
	if version.EffectiveUntil.Valid {
		response.EffectiveUntil = &version.EffectiveUntil.Time
	}
	if version.IndexError.Valid {
		response.IndexError = &version.IndexError.String
	}
	if version.IndexAttemptID.Valid {
		attemptID := version.IndexAttemptID
		response.IndexAttemptID = &attemptID
	}
	if version.IndexStartedAt.Valid {
		response.IndexStartedAt = &version.IndexStartedAt.Time
	}
	if version.IndexedAt.Valid {
		response.IndexedAt = &version.IndexedAt.Time
	}
	return response
}

func policyVersionIndexRecoverable(version db.PolicyVersion, now time.Time) bool {
	return version.IndexStatus == "PROCESSING" &&
		version.IndexStartedAt.Valid &&
		now.Sub(version.IndexStartedAt.Time) > policyIndexLease()
}

func policyIndexLease() time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("POLICY_INDEX_LEASE_SECONDS")), 10, 64)
	maxSeconds := int64(time.Duration(1<<63-1) / time.Second)
	if err != nil || seconds <= 0 || seconds > maxSeconds {
		seconds = defaultPolicyIndexLeaseSeconds
	}
	return time.Duration(seconds) * time.Second
}

func writeInvalidPolicyVersionRequest(w http.ResponseWriter, message string) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, message, nil))
}

func writePolicyVersionNotFound(w http.ResponseWriter) {
	httpapi.WriteError(w, httpapi.NewError(httpapi.CodePolicyNotFound, "", nil))
}
