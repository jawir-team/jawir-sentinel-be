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
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const caseEventEvidenceAdded = "EVIDENCE_ADDED"

// EvidenceStore runs evidence reads and writes atomically.
type EvidenceStore interface {
	RunEvidenceTx(context.Context, func(context.Context, EvidenceTxQueries) error) error
}

// EvidenceTxQueries deliberately excludes workflow status and outbox methods:
// adding text evidence must never transition a case or trigger re-analysis.
type EvidenceTxQueries interface {
	GetCase(context.Context, pgtype.UUID) (db.Case, error)
	ListCaseParticipants(context.Context, pgtype.UUID) ([]db.CaseParticipant, error)
	CreateEvidence(context.Context, db.CreateEvidenceParams) (db.CaseEvidence, error)
	ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error)
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
	GetUser(context.Context, pgtype.UUID) (db.User, error)
}

var _ EvidenceTxQueries = (*db.Queries)(nil)

type addCaseEvidenceRequest struct {
	EvidenceType string `json:"evidence_type"`
	Title        string `json:"title"`
	Content      string `json:"content"`
}

type evidenceSourceUserResponse struct {
	ID   pgtype.UUID `json:"id"`
	Name string      `json:"name"`
}

type caseEvidenceResponse struct {
	ID           pgtype.UUID                 `json:"id"`
	EvidenceType string                      `json:"evidence_type"`
	SourceType   string                      `json:"source_type"`
	SourceUser   *evidenceSourceUserResponse `json:"source_user"`
	Title        *string                     `json:"title"`
	Content      *string                     `json:"content"`
	FilePath     *string                     `json:"file_path"`
	MimeType     *string                     `json:"mime_type"`
	CreatedAt    time.Time                   `json:"created_at"`
}

// AddCaseEvidence adds text evidence without changing the case workflow state.
func AddCaseEvidence(store EvidenceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if isNilEvidenceStore(store) {
			logging.With(r.Context()).Error("add case evidence: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request addCaseEvidenceRequest
		if err := decodeEvidenceRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		request.EvidenceType = strings.ToUpper(strings.TrimSpace(request.EvidenceType))
		request.Title = strings.TrimSpace(request.Title)
		request.Content = strings.TrimSpace(request.Content)
		if !validEvidenceType(request.EvidenceType) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Evidence type is invalid.", nil))
			return
		}
		if request.Title == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Evidence title is required.", nil))
			return
		}
		if request.Content == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Evidence content is required.", nil))
			return
		}

		var response caseEvidenceResponse
		var apiErr *httpapi.APIError
		txErr := store.RunEvidenceTx(r.Context(), func(ctx context.Context, q EvidenceTxQueries) error {
			response, apiErr = runAddCaseEvidence(ctx, q, caseID, actor, request)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("add case evidence", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit case evidence", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: response})
	}
}

func runAddCaseEvidence(
	ctx context.Context,
	q EvidenceTxQueries,
	caseID pgtype.UUID,
	actor auth.User,
	request addCaseEvidenceRequest,
) (caseEvidenceResponse, *httpapi.APIError) {
	if isNilEvidenceTxQueries(q) {
		return caseEvidenceResponse{}, participantInternalError(errors.New("evidence transaction queries are not configured"))
	}

	sourceType, apiErr := authorizeEvidenceWrite(ctx, q, caseID, actor)
	if apiErr != nil {
		return caseEvidenceResponse{}, apiErr
	}

	evidenceID, err := newUnitUUID()
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}
	created, err := q.CreateEvidence(ctx, db.CreateEvidenceParams{
		ID:           evidenceID,
		CaseID:       caseID,
		SourceType:   sourceType,
		SourceUserID: actor.ID,
		EvidenceType: request.EvidenceType,
		Title:        pgtype.Text{String: request.Title, Valid: true},
		Content:      pgtype.Text{String: request.Content, Valid: true},
		FilePath:     pgtype.Text{Valid: false},
		MimeType:     pgtype.Text{Valid: false},
	})
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}
	sourceUser, err := q.GetUser(ctx, actor.ID)
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}

	metadata, err := json.Marshal(struct {
		EvidenceID string `json:"evidence_id"`
	}{EvidenceID: evidenceID.String()})
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}
	auditID, err := newUnitUUID()
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:        auditID,
		CaseID:    caseID,
		EventType: caseEventEvidenceAdded,
		ActorID:   actor.ID,
		ActorRole: nullableWorkflowActorRole(sourceType),
		Metadata:  metadata,
	}); err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}

	return newEvidenceResponse(created, &sourceUser), nil
}

// ListCaseEvidences returns case evidence in chronological order.
func ListCaseEvidences(store EvidenceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if isNilEvidenceStore(store) {
			logging.With(r.Context()).Error("list case evidences: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}

		var response []caseEvidenceResponse
		var apiErr *httpapi.APIError
		txErr := store.RunEvidenceTx(r.Context(), func(ctx context.Context, q EvidenceTxQueries) error {
			response, apiErr = runListCaseEvidences(ctx, q, caseID, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("list case evidences", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit case evidence list", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: response})
	}
}

func runListCaseEvidences(ctx context.Context, q EvidenceTxQueries, caseID pgtype.UUID, actor auth.User) ([]caseEvidenceResponse, *httpapi.APIError) {
	if isNilEvidenceTxQueries(q) {
		return nil, participantInternalError(errors.New("evidence transaction queries are not configured"))
	}

	if _, err := q.GetCase(ctx, caseID); errors.Is(err, pgx.ErrNoRows) {
		return nil, participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	} else if err != nil {
		return nil, participantInternalError(err)
	}
	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	if _, assigned := activeEvidenceActorRole(participants, actor.ID); !assigned && actor.SystemRole != auth.SystemRoleAdmin {
		return nil, participantAPIError(httpapi.CodeForbidden, "", nil)
	}

	evidences, err := q.ListCaseEvidences(ctx, caseID)
	if err != nil {
		return nil, participantInternalError(err)
	}
	response := make([]caseEvidenceResponse, 0, len(evidences))
	for _, evidence := range evidences {
		var sourceUser *db.User
		if evidence.SourceUserID.Valid {
			user, err := q.GetUser(ctx, evidence.SourceUserID)
			if err != nil {
				return nil, participantInternalError(err)
			}
			sourceUser = &user
		}
		response = append(response, newEvidenceResponse(evidence, sourceUser))
	}
	return response, nil
}

func activeEvidenceActorRole(participants []db.CaseParticipant, actorID pgtype.UUID) (string, bool) {
	role := ""
	for _, participant := range participants {
		if participant.Status != participantStatusActive || participant.UserID != actorID {
			continue
		}
		if !isCaseWorkflowRole(participant.Role) || role != "" {
			return "", false
		}
		role = participant.Role
	}
	return role, role != ""
}

func canAddEvidence(state workflow.State, sourceType string) bool {
	switch state {
	case workflow.StateDraft:
		return sourceType == caseRoleMaker
	case workflow.StateChecking, workflow.StateSigning, workflow.StateExecution, workflow.StateEscalationRequired:
		return true
	default:
		return false
	}
}

func validEvidenceType(evidenceType string) bool {
	switch evidenceType {
	case "COMMENT", "DOCUMENT", "LOG", "SCREENSHOT", "REFERENCE", "EXECUTION_RESULT":
		return true
	default:
		return false
	}
}

// decodeEvidenceRequest intentionally ignores unknown JSON fields. Evidence
// attribution is derived exclusively from the authenticated user's active case
// assignment, so client-supplied actor_role or source_type fields have no effect.
func decodeEvidenceRequest(w http.ResponseWriter, r *http.Request, request any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCaseBodyBytes)
	decoder := json.NewDecoder(r.Body)
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

func newEvidenceResponse(evidence db.CaseEvidence, sourceUser *db.User) caseEvidenceResponse {
	response := caseEvidenceResponse{
		ID:           evidence.ID,
		EvidenceType: evidence.EvidenceType,
		SourceType:   evidence.SourceType,
		CreatedAt:    evidence.CreatedAt,
	}
	if sourceUser != nil && evidence.SourceUserID.Valid {
		response.SourceUser = &evidenceSourceUserResponse{ID: evidence.SourceUserID, Name: sourceUser.Name}
	}
	if evidence.Title.Valid {
		value := evidence.Title.String
		response.Title = &value
	}
	if evidence.Content.Valid {
		value := evidence.Content.String
		response.Content = &value
	}
	if evidence.FilePath.Valid {
		value := evidence.FilePath.String
		response.FilePath = &value
	}
	if evidence.MimeType.Valid {
		value := evidence.MimeType.String
		response.MimeType = &value
	}
	return response
}

func isNilEvidenceStore(store EvidenceStore) bool {
	return store == nil || isNilInterface(store)
}

func isNilEvidenceTxQueries(q EvidenceTxQueries) bool {
	return q == nil || isNilInterface(q)
}
