package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	filestorage "github.com/jawir-team/jawir-sentinel-be/internal/storage"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

type evidenceUploadURLRequest struct {
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
}

type evidenceUploadURLResponse struct {
	UploadURL string `json:"upload_url"`
	FileKey   string `json:"file_key"`
}

type registerFileEvidenceRequest struct {
	FileKey      string `json:"file_key"`
	Title        string `json:"title"`
	EvidenceType string `json:"evidence_type"`
}

// IssueEvidenceUploadURL authorizes an evidence upload and returns a
// case-scoped, content-type-pinned GCS upload URL.
func IssueEvidenceUploadURL(store EvidenceStore, files filestorage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if isNilEvidenceStore(store) {
			logging.With(r.Context()).Error("issue evidence upload URL: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		if isNilFileStorage(files) {
			logging.With(r.Context()).Error("issue evidence upload URL: file storage is not configured")
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInternalError, "File upload is not configured.", nil))
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request evidenceUploadURLRequest
		if err := decodeEvidenceRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		request.FileName = strings.TrimSpace(request.FileName)
		request.MimeType = normalizeEvidenceMIME(request.MimeType)
		if !validEvidenceFileName(request.FileName) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "File name is invalid.", nil))
			return
		}
		if !validEvidenceMIME(request.MimeType) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Unsupported file type.", nil))
			return
		}

		var apiErr *httpapi.APIError
		txErr := store.RunEvidenceTx(r.Context(), func(ctx context.Context, q EvidenceTxQueries) error {
			_, apiErr = authorizeEvidenceWrite(ctx, q, caseID, actor)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("authorize evidence upload URL", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("authorize evidence upload URL transaction", "case_id", caseID.String(), "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		objectID, err := newUnitUUID()
		if err != nil {
			logging.With(r.Context()).Error("generate evidence upload object ID", "case_id", caseID.String(), "error", err)
			httpapi.WriteError(w, nil)
			return
		}
		fileKey := fmt.Sprintf("cases/%s/evidence/%s-%s", caseID.String(), objectID.String(), request.FileName)
		uploadURL, err := files.SignUploadURL(r.Context(), fileKey, request.MimeType)
		if err != nil {
			logging.With(r.Context()).Error("sign evidence upload URL", "case_id", caseID.String(), "file_key", fileKey, "error", err)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: evidenceUploadURLResponse{
			UploadURL: uploadURL,
			FileKey:   fileKey,
		}})
	}
}

// RegisterFileEvidence validates an uploaded GCS object and records it as case
// evidence without changing workflow state or scheduling analysis.
func RegisterFileEvidence(store EvidenceStore, files filestorage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if isNilEvidenceStore(store) {
			logging.With(r.Context()).Error("register file evidence: store is not configured")
			httpapi.WriteError(w, nil)
			return
		}
		if isNilFileStorage(files) {
			logging.With(r.Context()).Error("register file evidence: file storage is not configured")
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInternalError, "File upload is not configured.", nil))
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		var request registerFileEvidenceRequest
		if err := decodeEvidenceRequest(w, r, &request); err != nil {
			writeInvalidCaseRequest(w, "Invalid request body.")
			return
		}
		request.FileKey = strings.TrimSpace(request.FileKey)
		request.Title = strings.TrimSpace(request.Title)
		request.EvidenceType = strings.ToUpper(strings.TrimSpace(request.EvidenceType))
		if request.Title == "" {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Evidence title is required.", nil))
			return
		}
		if !validEvidenceType(request.EvidenceType) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "Evidence type is invalid.", nil))
			return
		}
		prefix := evidenceObjectPrefix(caseID)
		if !strings.HasPrefix(request.FileKey, prefix) || len(request.FileKey) == len(prefix) {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeValidationError, "file_key does not belong to this case.", nil))
			return
		}

		var response caseEvidenceResponse
		var apiErr *httpapi.APIError
		txErr := store.RunEvidenceTx(r.Context(), func(ctx context.Context, q EvidenceTxQueries) error {
			response, apiErr = runRegisterFileEvidence(ctx, q, files, caseID, actor, request)
			if apiErr != nil {
				return apiErr
			}
			return nil
		})
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("register file evidence", "case_id", caseID.String(), "file_key", request.FileKey, "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}
		if txErr != nil {
			logging.With(r.Context()).Error("commit file evidence", "case_id", caseID.String(), "file_key", request.FileKey, "error", txErr)
			httpapi.WriteError(w, nil)
			return
		}

		httpapi.WriteJSON(w, http.StatusCreated, httpapi.SuccessEnvelope{Data: response})
	}
}

func runRegisterFileEvidence(
	ctx context.Context,
	q EvidenceTxQueries,
	files filestorage.Store,
	caseID pgtype.UUID,
	actor auth.User,
	request registerFileEvidenceRequest,
) (caseEvidenceResponse, *httpapi.APIError) {
	sourceType, apiErr := authorizeEvidenceWrite(ctx, q, caseID, actor)
	if apiErr != nil {
		return caseEvidenceResponse{}, apiErr
	}

	attrs, err := files.StatObject(ctx, request.FileKey)
	if errors.Is(err, filestorage.ErrObjectNotFound) {
		return caseEvidenceResponse{}, participantAPIError(httpapi.CodeValidationError, "Uploaded file was not found.", nil)
	}
	if err != nil {
		return caseEvidenceResponse{}, participantInternalError(err)
	}
	mimeType := normalizeEvidenceMIME(attrs.ContentType)
	if !validEvidenceMIME(mimeType) {
		return caseEvidenceResponse{}, participantAPIError(httpapi.CodeValidationError, "Unsupported file type.", nil)
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
		Content:      pgtype.Text{Valid: false},
		FilePath:     pgtype.Text{String: request.FileKey, Valid: true},
		MimeType:     pgtype.Text{String: mimeType, Valid: true},
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
		FileKey    string `json:"file_key"`
		MimeType   string `json:"mime_type"`
	}{
		EvidenceID: evidenceID.String(),
		FileKey:    request.FileKey,
		MimeType:   mimeType,
	})
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

func authorizeEvidenceWrite(ctx context.Context, q EvidenceTxQueries, caseID pgtype.UUID, actor auth.User) (string, *httpapi.APIError) {
	if isNilEvidenceTxQueries(q) {
		return "", participantInternalError(errors.New("evidence transaction queries are not configured"))
	}
	stored, err := q.GetCase(ctx, caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", participantAPIError(httpapi.CodeCaseNotFound, "", nil)
	}
	if err != nil {
		return "", participantInternalError(err)
	}
	participants, err := q.ListCaseParticipants(ctx, caseID)
	if err != nil {
		return "", participantInternalError(err)
	}
	sourceType, assigned := activeEvidenceActorRole(participants, actor.ID)
	if !assigned || !canAddEvidence(workflow.State(stored.Status), sourceType) {
		return "", participantAPIError(httpapi.CodeForbidden, "", nil)
	}
	return sourceType, nil
}

func evidenceObjectPrefix(caseID pgtype.UUID) string {
	return fmt.Sprintf("cases/%s/evidence/", caseID.String())
}

func normalizeEvidenceMIME(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validEvidenceMIME(mimeType string) bool {
	switch mimeType {
	case "application/pdf", "image/jpeg", "image/png":
		return true
	default:
		return false
	}
}

func validEvidenceFileName(fileName string) bool {
	return fileName != "" && utf8.RuneCountInString(fileName) <= 255 && fileName != "." && fileName != ".." &&
		!strings.Contains(fileName, "/") && !strings.Contains(fileName, "\\")
}

func isNilFileStorage(files filestorage.Store) bool {
	return files == nil || isNilInterface(files)
}
