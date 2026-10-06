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
	maxPolicyIndexResultBodyBytes = 1 << 20
	policyIndexEventClaimed       = "POLICY_INDEX_CLAIMED"
	policyIndexEventCompleted     = "POLICY_INDEX_COMPLETED"
	policyVersionEventActivated   = "POLICY_VERSION_ACTIVATED"
	policyVersionEventSuperseded  = "POLICY_VERSION_SUPERSEDED"
	policyVersionStatusDraft      = "DRAFT"
	policyVersionStatusActive     = "ACTIVE"
	policyVersionStatusSuperseded = "SUPERSEDED"
	policyIndexStatusNotStarted   = "NOT_STARTED"
	policyIndexStatusProcessing   = "PROCESSING"
	policyIndexStatusReady        = "READY"
	policyIndexStatusFailed       = "FAILED"
)

// PolicyActivationTxQueries is the database boundary for claiming an index
// lease, completing that lease, and atomically activating a policy version.
type PolicyActivationTxQueries interface {
	GetPolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error)
	GetActivePolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error)
	ClaimPolicyVersionIndex(context.Context, db.ClaimPolicyVersionIndexParams) (db.PolicyVersion, error)
	CompletePolicyVersionIndex(context.Context, db.CompletePolicyVersionIndexParams) (db.PolicyVersion, error)
	UpdatePolicyVersionStatus(context.Context, db.UpdatePolicyVersionStatusParams) (db.PolicyVersion, error)
	AppendPolicyAuditEvent(context.Context, db.AppendPolicyAuditEventParams) (db.AuditEvent, error)
}

var _ PolicyActivationTxQueries = (*db.Queries)(nil)

// PolicyActivationStore runs every policy activation workflow atomically.
type PolicyActivationStore interface {
	RunPolicyActivationTx(context.Context, func(context.Context, PolicyActivationTxQueries) error) error
}

var _ PolicyActivationStore = (*TxQueries)(nil)

type policyIndexResultRequest struct {
	IndexAttemptID string `json:"index_attempt_id"`
	IndexStatus    string `json:"index_status"`
	IndexError     string `json:"index_error"`
}

// ClaimPolicyVersionIndex acquires or recovers the indexing lease for an
// effective DRAFT policy version. Authorization is owned by RequireAdmin.
func ClaimPolicyVersionIndex(store PolicyActivationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("claim policy version index: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		policyID, versionID, ok := policyActivationIDs(w, r)
		if !ok {
			return
		}

		var claimed *db.PolicyVersion
		var apiErr *httpapi.APIError
		txErr := store.RunPolicyActivationTx(r.Context(), func(ctx context.Context, q PolicyActivationTxQueries) error {
			claimed, apiErr = runClaimPolicyVersionIndex(ctx, q, policyID, versionID, actor, time.Now())
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if writePolicyActivationTxError(w, r, "claim policy version index", policyID, versionID, apiErr, txErr) {
			return
		}
		if claimed == nil {
			logging.With(r.Context()).Error("claim policy version index: transaction returned no version", "policy_id", policyID.String(), "policy_version_id", versionID.String())
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newPolicyVersionResponse(*claimed)})
	}
}

// CompletePolicyVersionIndex records READY or FAILED for the current indexing
// attempt. The attempt predicate in SQL prevents a recovered lease from being
// overwritten by a stale worker. Authorization is owned by RequireAdmin.
func CompletePolicyVersionIndex(store PolicyActivationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("complete policy version index: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		policyID, versionID, ok := policyActivationIDs(w, r)
		if !ok {
			return
		}

		var request policyIndexResultRequest
		if err := decodePolicyIndexResultRequest(w, r, &request); err != nil {
			writeInvalidPolicyVersionRequest(w, "Invalid request body.")
			return
		}
		request.IndexAttemptID = strings.TrimSpace(request.IndexAttemptID)
		attemptID, err := parseUserUUID(request.IndexAttemptID)
		if err != nil {
			writeInvalidPolicyVersionRequest(w, "index_attempt_id must be a valid UUID.")
			return
		}
		request.IndexStatus = strings.TrimSpace(request.IndexStatus)
		if request.IndexStatus != policyIndexStatusReady && request.IndexStatus != policyIndexStatusFailed {
			writeInvalidPolicyVersionRequest(w, "index_status must be READY or FAILED.")
			return
		}

		var completed *db.PolicyVersion
		var apiErr *httpapi.APIError
		txErr := store.RunPolicyActivationTx(r.Context(), func(ctx context.Context, q PolicyActivationTxQueries) error {
			completed, apiErr = runCompletePolicyVersionIndex(ctx, q, policyID, versionID, actor, attemptID, request)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if writePolicyActivationTxError(w, r, "complete policy version index", policyID, versionID, apiErr, txErr) {
			return
		}
		if completed == nil {
			logging.With(r.Context()).Error("complete policy version index: transaction returned no version", "policy_id", policyID.String(), "policy_version_id", versionID.String())
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newPolicyVersionResponse(*completed)})
	}
}

// ActivatePolicyVersion atomically supersedes the old ACTIVE version and
// activates an effective, indexed DRAFT. Authorization is owned by RequireAdmin.
func ActivatePolicyVersion(store PolicyActivationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if store == nil || isNilInterface(store) {
			logging.With(r.Context()).Error("activate policy version: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		policyID, versionID, ok := policyActivationIDs(w, r)
		if !ok {
			return
		}

		var activated *db.PolicyVersion
		var apiErr *httpapi.APIError
		txErr := store.RunPolicyActivationTx(r.Context(), func(ctx context.Context, q PolicyActivationTxQueries) error {
			activated, apiErr = runActivatePolicyVersion(ctx, q, policyID, versionID, actor, time.Now())
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if writePolicyActivationTxError(w, r, "activate policy version", policyID, versionID, apiErr, txErr) {
			return
		}
		if activated == nil {
			logging.With(r.Context()).Error("activate policy version: transaction returned no version", "policy_id", policyID.String(), "policy_version_id", versionID.String())
			httpapi.WriteError(w, nil)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newPolicyVersionResponse(*activated)})
	}
}

func runClaimPolicyVersionIndex(
	ctx context.Context,
	q PolicyActivationTxQueries,
	policyID, versionID pgtype.UUID,
	actor auth.User,
	now time.Time,
) (*db.PolicyVersion, *httpapi.APIError) {
	version, apiErr := lockedPolicyVersion(ctx, q, policyID, versionID)
	if apiErr != nil {
		return nil, apiErr
	}
	if version.Status != policyVersionStatusDraft {
		return nil, policyActivationConflict("policy version is not a draft")
	}
	if apiErr := validatePolicyVersionEffective(version, now); apiErr != nil {
		return nil, apiErr
	}

	switch version.IndexStatus {
	case policyIndexStatusNotStarted, policyIndexStatusFailed:
	case policyIndexStatusProcessing:
		if !policyVersionIndexRecoverable(version, now) {
			return nil, policyActivationConflict("indexing already in progress")
		}
	case policyIndexStatusReady:
		return nil, policyActivationConflict("already indexed")
	default:
		return nil, policyActivationInternalError(errors.New("unknown policy index status: " + version.IndexStatus))
	}

	attemptID, err := newUnitUUID()
	if err != nil {
		return nil, policyActivationInternalError(err)
	}
	claimed, err := q.ClaimPolicyVersionIndex(ctx, db.ClaimPolicyVersionIndexParams{
		ID:             version.ID,
		IndexAttemptID: attemptID,
	})
	if err != nil {
		return nil, policyActivationInternalError(err)
	}
	if apiErr := appendPolicyActivationAuditEvent(ctx, q, policyID, version.ID, actor.ID, policyIndexEventClaimed, []byte(`{}`)); apiErr != nil {
		return nil, apiErr
	}
	return &claimed, nil
}

func runCompletePolicyVersionIndex(
	ctx context.Context,
	q PolicyActivationTxQueries,
	policyID, versionID pgtype.UUID,
	actor auth.User,
	attemptID pgtype.UUID,
	request policyIndexResultRequest,
) (*db.PolicyVersion, *httpapi.APIError) {
	version, apiErr := lockedPolicyVersion(ctx, q, policyID, versionID)
	if apiErr != nil {
		return nil, apiErr
	}
	if version.IndexStatus != policyIndexStatusProcessing {
		return nil, policyActivationConflict("indexing is not in progress")
	}

	indexError := pgtype.Text{}
	if request.IndexStatus == policyIndexStatusFailed {
		indexError = pgtype.Text{String: strings.TrimSpace(request.IndexError), Valid: true}
	}
	completed, err := q.CompletePolicyVersionIndex(ctx, db.CompletePolicyVersionIndexParams{
		ID:             version.ID,
		IndexStatus:    request.IndexStatus,
		IndexError:     indexError,
		IndexAttemptID: attemptID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, policyActivationConflict("stale attempt superseded")
	}
	if err != nil {
		return nil, policyActivationInternalError(err)
	}
	metadata, err := json.Marshal(struct {
		IndexStatus string `json:"index_status"`
	}{IndexStatus: request.IndexStatus})
	if err != nil {
		return nil, policyActivationInternalError(err)
	}
	if apiErr := appendPolicyActivationAuditEvent(ctx, q, policyID, version.ID, actor.ID, policyIndexEventCompleted, metadata); apiErr != nil {
		return nil, apiErr
	}
	return &completed, nil
}

func runActivatePolicyVersion(
	ctx context.Context,
	q PolicyActivationTxQueries,
	policyID, versionID pgtype.UUID,
	actor auth.User,
	now time.Time,
) (*db.PolicyVersion, *httpapi.APIError) {
	target, apiErr := lockedPolicyVersion(ctx, q, policyID, versionID)
	if apiErr != nil {
		return nil, apiErr
	}
	if target.Status != policyVersionStatusDraft {
		return nil, policyActivationConflict("policy version is not a draft")
	}
	if target.IndexStatus != policyIndexStatusReady {
		return nil, policyActivationConflict("policy version is not ready for activation")
	}
	if apiErr := validatePolicyVersionEffective(target, now); apiErr != nil {
		return nil, apiErr
	}

	active, err := q.GetActivePolicyVersionForUpdate(ctx, policyID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, policyActivationInternalError(err)
	}
	if err == nil && active.ID != target.ID {
		if _, err := q.UpdatePolicyVersionStatus(ctx, db.UpdatePolicyVersionStatusParams{
			ID:     active.ID,
			Status: policyVersionStatusSuperseded,
		}); err != nil {
			return nil, policyActivationInternalError(err)
		}
		if apiErr := appendPolicyActivationAuditEvent(ctx, q, policyID, active.ID, actor.ID, policyVersionEventSuperseded, []byte(`{}`)); apiErr != nil {
			return nil, apiErr
		}
	}

	// There is no externally observable authority gap: superseding the old row
	// and activating this target commit together, and any failure rolls both back.
	activated, err := q.UpdatePolicyVersionStatus(ctx, db.UpdatePolicyVersionStatusParams{
		ID:     target.ID,
		Status: policyVersionStatusActive,
	})
	if err != nil {
		return nil, policyActivationInternalError(err)
	}
	if apiErr := appendPolicyActivationAuditEvent(ctx, q, policyID, target.ID, actor.ID, policyVersionEventActivated, []byte(`{}`)); apiErr != nil {
		return nil, apiErr
	}
	return &activated, nil
}

func lockedPolicyVersion(
	ctx context.Context,
	q PolicyActivationTxQueries,
	policyID, versionID pgtype.UUID,
) (db.PolicyVersion, *httpapi.APIError) {
	if q == nil || isNilInterface(q) {
		return db.PolicyVersion{}, policyActivationInternalError(errors.New("policy activation transaction queries are not configured"))
	}
	version, err := q.GetPolicyVersionForUpdate(ctx, versionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && version.PolicyID != policyID) {
		return db.PolicyVersion{}, httpapi.NewError(httpapi.CodePolicyNotFound, "", nil)
	}
	if err != nil {
		return db.PolicyVersion{}, policyActivationInternalError(err)
	}
	return version, nil
}

func validatePolicyVersionEffective(version db.PolicyVersion, now time.Time) *httpapi.APIError {
	if version.EffectiveFrom.Valid && version.EffectiveFrom.Time.After(now) {
		return policyActivationConflict("not yet effective")
	}
	if version.EffectiveUntil.Valid && !version.EffectiveUntil.Time.After(now) {
		return policyActivationConflict("expired")
	}
	return nil
}

func appendPolicyActivationAuditEvent(
	ctx context.Context,
	q PolicyActivationTxQueries,
	policyID, versionID, actorID pgtype.UUID,
	eventType string,
	metadata []byte,
) *httpapi.APIError {
	auditID, err := newUnitUUID()
	if err != nil {
		return policyActivationInternalError(err)
	}
	if _, err := q.AppendPolicyAuditEvent(ctx, db.AppendPolicyAuditEventParams{
		ID:              auditID,
		PolicyID:        policyID,
		PolicyVersionID: versionID,
		EventType:       eventType,
		ActorID:         actorID,
		Metadata:        metadata,
	}); err != nil {
		return policyActivationInternalError(err)
	}
	return nil
}

func policyActivationIDs(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	policyID, err := parseUserUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeInvalidPolicyVersionRequest(w, "id must be a valid policy UUID.")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	versionID, err := parseUserUUID(chi.URLParam(r, "version_id"))
	if err != nil {
		writeInvalidPolicyVersionRequest(w, "version_id must be a valid UUID.")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return policyID, versionID, true
}

func decodePolicyIndexResultRequest(w http.ResponseWriter, r *http.Request, request *policyIndexResultRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxPolicyIndexResultBodyBytes)
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

func writePolicyActivationTxError(
	w http.ResponseWriter,
	r *http.Request,
	operation string,
	policyID, versionID pgtype.UUID,
	apiErr *httpapi.APIError,
	txErr error,
) bool {
	if apiErr != nil {
		if apiErr.Code == httpapi.CodeInternalError {
			logging.With(r.Context()).Error(operation, "policy_id", policyID.String(), "policy_version_id", versionID.String(), "error", apiErr)
		}
		httpapi.WriteError(w, apiErr)
		return true
	}
	if txErr != nil {
		logging.With(r.Context()).Error("commit "+operation, "policy_id", policyID.String(), "policy_version_id", versionID.String(), "error", txErr)
		httpapi.WriteError(w, nil)
		return true
	}
	return false
}

func policyActivationConflict(message string) *httpapi.APIError {
	return httpapi.NewError(httpapi.CodeConflict, message, nil)
}

func policyActivationInternalError(err error) *httpapi.APIError {
	return httpapi.WrapError(httpapi.CodeInternalError, "", nil, err)
}
