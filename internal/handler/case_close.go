package handler

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const caseEventClosed = "CASE_CLOSED"

// CloseCaseStore runs the complete case closure workflow atomically.
type CloseCaseStore interface {
	RunCloseTx(context.Context, func(context.Context, CloseTxQueries) error) error
}

type closeCaseRequest struct {
	Reason string `json:"reason"`
}

// CloseCase closes a case when no AI analysis or execution is still running.
func CloseCase(store CloseCaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if isNilCloseCaseStore(store) {
			logging.With(r.Context()).Error("close case: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request closeCaseRequest
		if err := decodeCaseRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		reason := strings.TrimSpace(request.Reason)
		if reason == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Close reason is required.", nil))
			return
		}

		var closed *db.Case
		var apiErr *httpapi.APIError
		txErr := store.RunCloseTx(r.Context(), func(ctx context.Context, q CloseTxQueries) error {
			closed, apiErr = runCloseCase(ctx, q, caseID, actor, reason)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("close case", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit case closure", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}
		if closed == nil {
			logging.With(r.Context()).Error("close case: transaction returned no case", "case_id", caseID.String())
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: newCaseResponse(*closed)})
	}
}

func runCloseCase(ctx context.Context, q CloseTxQueries, caseID pgtype.UUID, actor auth.User, reason string) (*db.Case, *httpapi.APIError) {
	if isNilCloseTxQueries(q) {
		return nil, participantInternalError(errors.New("close transaction queries are not configured"))
	}

	stored, err := q.GetCaseForUpdate(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return nil, participantInternalError(err)
	}

	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	if _, allowed := participantActorRole(participants, actor); !allowed {
		return nil, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}

	if _, err := workflow.Transition(workflow.State(stored.Status), workflow.EventClose); workflow.IsInvalidTransition(err) {
		return nil, participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
	} else if err != nil {
		return nil, participantInternalError(err)
	}

	generating, err := q.ExistsGeneratingAnalysis(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	if generating {
		return nil, participantAPIError(httpapi.CodeInvalidStateTransition, "AI analysis is still running; wait and retry.", nil)
	}

	running, err := q.ExistsRunningExecution(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	if running {
		return nil, participantAPIError(httpapi.CodeInvalidStateTransition, "Execution is in progress; wait and retry.", nil)
	}

	// Closing only records the workflow transition. It intentionally does not
	// cancel AI jobs or executions; active work must finish before this point.
	actorRole, _ := participantActorRole(participants, actor)
	closed, err := q.CloseCase(ctx, db.CloseCaseParams{
		ID:          caseID,
		ClosedBy:    actor.ID,
		CloseReason: strings.TrimSpace(reason),
	})
	if err != nil {
		return nil, participantInternalError(err)
	}

	auditID, err := newUnitUUID()
	if err != nil {
		return nil, participantInternalError(err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:        auditID,
		CaseID:    caseID,
		EventType: caseEventClosed,
		ActorID:   actor.ID,
		ActorRole: nullableWorkflowActorRole(actorRole),
		Metadata:  []byte(`{}`),
	}); err != nil {
		return nil, participantInternalError(err)
	}

	return &closed, nil
}

func isNilCloseCaseStore(store CloseCaseStore) bool {
	return store == nil || isNilInterface(store)
}

func isNilCloseTxQueries(q CloseTxQueries) bool {
	return q == nil || isNilInterface(q)
}

func isNilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
