package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	filestorage "github.com/jawir-team/jawir-sentinel-be/internal/storage"
)

type fakeFileStorage struct {
	signedURL string
	signErr   error
	objects   map[string]filestorage.ObjectAttrs
	statErr   error

	signCalls int
	signKey   string
	signMIME  string
	statCalls int
	statKey   string
}

var _ filestorage.Store = (*fakeFileStorage)(nil)

func (f *fakeFileStorage) SignUploadURL(_ context.Context, key, contentType string) (string, error) {
	f.signCalls++
	f.signKey = key
	f.signMIME = contentType
	if f.signErr != nil {
		return "", f.signErr
	}
	if f.signedURL == "" {
		return "https://storage.example.test/upload", nil
	}
	return f.signedURL, nil
}

func (f *fakeFileStorage) StatObject(_ context.Context, key string) (filestorage.ObjectAttrs, error) {
	f.statCalls++
	f.statKey = key
	if f.statErr != nil {
		return filestorage.ObjectAttrs{}, f.statErr
	}
	attrs, ok := f.objects[key]
	if !ok {
		return filestorage.ObjectAttrs{}, filestorage.ErrObjectNotFound
	}
	return attrs, nil
}

func serveEvidenceUploadURL(actor *auth.User, queries *fakeEvidenceTxQueries, files filestorage.Store, caseID, body string) (*httptest.ResponseRecorder, *fakeEvidenceStore) {
	store := &fakeEvidenceStore{queries: queries}
	request := caseRequestWithID(http.MethodPost, "/api/v1/cases/"+caseID+"/evidences/upload-url", caseID, body, actor)
	response := httptest.NewRecorder()
	handler.IssueEvidenceUploadURL(store, files).ServeHTTP(response, request)
	return response, store
}

func serveRegisterFileEvidence(actor *auth.User, queries *fakeEvidenceTxQueries, files filestorage.Store, caseID, body string) (*httptest.ResponseRecorder, *fakeEvidenceStore) {
	store := &fakeEvidenceStore{queries: queries}
	request := caseRequestWithID(http.MethodPost, "/api/v1/cases/"+caseID+"/evidences/file", caseID, body, actor)
	response := httptest.NewRecorder()
	handler.RegisterFileEvidence(store, files).ServeHTTP(response, request)
	return response, store
}

func TestIssueEvidenceUploadURLHappyPath(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := evidenceTestQueries(actor, "CHECKER", "CHECKING")
	files := &fakeFileStorage{signedURL: "https://storage.example.test/signed-put"}
	caseID := handlerTestUUID(4).String()

	response, store := serveEvidenceUploadURL(&actor, queries, files, caseID, `{"file_name":" settlement-log.pdf ","mime_type":" Application/PDF "}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			UploadURL string `json:"upload_url"`
			FileKey   string `json:"file_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	prefix := "cases/" + caseID + "/evidence/"
	if body.Data.UploadURL == "" || !strings.HasPrefix(body.Data.FileKey, prefix) || !strings.HasSuffix(body.Data.FileKey, "-settlement-log.pdf") {
		t.Errorf("upload response = %+v, want signed URL and key under %q", body.Data, prefix)
	}
	if files.signCalls != 1 || files.signKey != body.Data.FileKey || files.signMIME != "application/pdf" {
		t.Errorf("sign call = %d key=%q MIME=%q, want one response key with normalized PDF MIME", files.signCalls, files.signKey, files.signMIME)
	}
	if store.calls != 1 || queries.getCaseCalls != 1 || queries.listParticipantCalls != 1 {
		t.Errorf("authorization calls = store %d case %d participants %d, want 1/1/1", store.calls, queries.getCaseCalls, queries.listParticipantCalls)
	}
}

func TestIssueEvidenceUploadURLRejectsUnsupportedMIME(t *testing.T) {
	for _, mimeType := range []string{"text/plain", "application/zip"} {
		t.Run(mimeType, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
			files := &fakeFileStorage{}
			body := `{"file_name":"evidence.bin","mime_type":"` + mimeType + `"}`

			response, store := serveEvidenceUploadURL(&actor, queries, files, handlerTestUUID(4).String(), body)

			assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
			if store.calls != 0 || files.signCalls != 0 {
				t.Errorf("calls = store %d sign %d, want no authorization or signing", store.calls, files.signCalls)
			}
		})
	}
}

func TestIssueEvidenceUploadURLRejectsInvalidFileName(t *testing.T) {
	for _, fileName := range []string{"dir/evidence.pdf", `dir\evidence.pdf`, ".."} {
		t.Run(fileName, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
			files := &fakeFileStorage{}
			bodyBytes, err := json.Marshal(map[string]string{"file_name": fileName, "mime_type": "application/pdf"})
			if err != nil {
				t.Fatal(err)
			}

			response, store := serveEvidenceUploadURL(&actor, queries, files, handlerTestUUID(4).String(), string(bodyBytes))

			assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
			if store.calls != 0 || files.signCalls != 0 {
				t.Errorf("calls = store %d sign %d, want no authorization or signing", store.calls, files.signCalls)
			}
		})
	}
}

func TestIssueEvidenceUploadURLAuthorizationMatrix(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		status       string
		participants bool
		wantStatus   int
	}{
		{name: "no active assignment", role: "CHECKER", status: "CHECKING", participants: false, wantStatus: http.StatusForbidden},
		{name: "submitted", role: "MAKER", status: "SUBMITTED", participants: true, wantStatus: http.StatusForbidden},
		{name: "closed", role: "MAKER", status: "CLOSED", participants: true, wantStatus: http.StatusForbidden},
		{name: "draft non-maker", role: "CHECKER", status: "DRAFT", participants: true, wantStatus: http.StatusForbidden},
		{name: "draft maker", role: "MAKER", status: "DRAFT", participants: true, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actor := caseTestUser(auth.SystemRoleUser)
			queries := evidenceTestQueries(actor, tt.role, tt.status)
			if !tt.participants {
				queries.participants = nil
			}
			files := &fakeFileStorage{}

			response, _ := serveEvidenceUploadURL(&actor, queries, files, handlerTestUUID(4).String(), `{"file_name":"photo.jpg","mime_type":"image/jpeg"}`)

			if tt.wantStatus == http.StatusOK {
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
				}
				if files.signCalls != 1 {
					t.Errorf("sign calls = %d, want 1", files.signCalls)
				}
				return
			}
			assertCaseTypeAPIError(t, response, tt.wantStatus, httpapi.CodeForbidden)
			if files.signCalls != 0 {
				t.Errorf("sign calls = %d, want 0", files.signCalls)
			}
		})
	}
}

func TestIssueEvidenceUploadURLEdgeFailures(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	validBody := `{"file_name":"evidence.png","mime_type":"image/png"}`

	t.Run("unauthenticated", func(t *testing.T) {
		queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
		response, store := serveEvidenceUploadURL(nil, queries, &fakeFileStorage{}, handlerTestUUID(4).String(), validBody)
		assertCaseTypeAPIError(t, response, http.StatusUnauthorized, httpapi.CodeUnauthorized)
		if store.calls != 0 {
			t.Errorf("transaction calls = %d, want 0", store.calls)
		}
	})

	t.Run("invalid case id", func(t *testing.T) {
		queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
		response, store := serveEvidenceUploadURL(&actor, queries, &fakeFileStorage{}, "not-a-uuid", validBody)
		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeInvalidRequest)
		if store.calls != 0 {
			t.Errorf("transaction calls = %d, want 0", store.calls)
		}
	})

	t.Run("nil storage", func(t *testing.T) {
		queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
		response, store := serveEvidenceUploadURL(&actor, queries, nil, handlerTestUUID(4).String(), validBody)
		assertCaseTypeAPIError(t, response, http.StatusInternalServerError, httpapi.CodeInternalError)
		if store.calls != 0 {
			t.Errorf("transaction calls = %d, want 0", store.calls)
		}
	})
}

func TestRegisterFileEvidenceHappyPathAndAudit(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := evidenceTestQueries(actor, "CHECKER", "CHECKING")
	originalStatus := queries.caseResult.Status
	caseID := handlerTestUUID(4).String()
	fileKey := "cases/" + caseID + "/evidence/upload-id-settlement-log.pdf"
	files := &fakeFileStorage{objects: map[string]filestorage.ObjectAttrs{
		fileKey: {ContentType: " Application/PDF ", Size: 512},
	}}
	body := `{"file_key":"` + fileKey + `","title":" Settlement Log ","evidence_type":"document","actor_role":"MAKER","source_type":"SYSTEM"}`

	response, store := serveRegisterFileEvidence(&actor, queries, files, caseID, body)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var responseBody struct {
		Data struct {
			EvidenceType string  `json:"evidence_type"`
			SourceType   string  `json:"source_type"`
			FilePath     *string `json:"file_path"`
			MimeType     *string `json:"mime_type"`
			Content      *string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if responseBody.Data.SourceType != "CHECKER" || responseBody.Data.EvidenceType != "DOCUMENT" || responseBody.Data.FilePath == nil || *responseBody.Data.FilePath != fileKey || responseBody.Data.MimeType == nil || *responseBody.Data.MimeType != "application/pdf" || responseBody.Data.Content != nil {
		t.Errorf("response data = %+v, want server-derived CHECKER file evidence", responseBody.Data)
	}
	if queries.createCalls != 1 || queries.createArg.SourceType != "CHECKER" || queries.createArg.SourceUserID != actor.ID || !queries.createArg.FilePath.Valid || queries.createArg.FilePath.String != fileKey || !queries.createArg.MimeType.Valid || queries.createArg.MimeType.String != "application/pdf" || queries.createArg.Content.Valid {
		t.Errorf("CreateEvidence call/arg = %d/%+v, want persisted file and normalized MIME attributed to CHECKER", queries.createCalls, queries.createArg)
	}
	if queries.auditCalls != 1 || queries.auditArgs[0].EventType != "EVIDENCE_ADDED" || queries.auditArgs[0].ActorRole.String != "CHECKER" {
		t.Fatalf("audit call/arg = %d/%+v, want one EVIDENCE_ADDED by CHECKER", queries.auditCalls, queries.auditArgs)
	}
	var metadata struct {
		EvidenceID string `json:"evidence_id"`
		FileKey    string `json:"file_key"`
		MimeType   string `json:"mime_type"`
	}
	if err := json.Unmarshal(queries.auditArgs[0].Metadata, &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata.EvidenceID != queries.createArg.ID.String() || metadata.FileKey != fileKey || metadata.MimeType != "application/pdf" {
		t.Errorf("audit metadata = %+v, want created ID, file key, and MIME", metadata)
	}
	if files.statCalls != 1 || files.statKey != fileKey || store.calls != 1 {
		t.Errorf("storage/transaction calls = stat %d key %q tx %d, want 1/%q/1", files.statCalls, files.statKey, store.calls, fileKey)
	}
	if queries.caseResult.Status != originalStatus {
		t.Errorf("case status = %q, want unchanged %q", queries.caseResult.Status, originalStatus)
	}
	// EvidenceTxQueries intentionally has no workflow update or outbox method,
	// so this fake cannot observe (or permit) a workflow side effect.
}

func TestRegisterFileEvidenceRejectsForeignKeys(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4).String()
	for _, tt := range []struct {
		name    string
		fileKey string
	}{
		{name: "different case", fileKey: "cases/00000000-0000-0000-0000-000000000005/evidence/file.pdf"},
		{name: "outside prefix", fileKey: "arbitrary/file.pdf"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
			files := &fakeFileStorage{}
			body := `{"file_key":"` + tt.fileKey + `","title":"Evidence","evidence_type":"DOCUMENT"}`

			response, store := serveRegisterFileEvidence(&actor, queries, files, caseID, body)

			assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
			if store.calls != 0 || files.statCalls != 0 {
				t.Errorf("calls = tx %d stat %d, want no authorization or object lookup", store.calls, files.statCalls)
			}
		})
	}
}

func TestRegisterFileEvidenceRejectsMissingOrUnsupportedObject(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4).String()
	fileKey := "cases/" + caseID + "/evidence/upload-id-file.dat"
	body := `{"file_key":"` + fileKey + `","title":"Evidence","evidence_type":"DOCUMENT"}`

	t.Run("missing", func(t *testing.T) {
		queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
		files := &fakeFileStorage{objects: map[string]filestorage.ObjectAttrs{}}
		response, _ := serveRegisterFileEvidence(&actor, queries, files, caseID, body)
		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
		assertNoEvidenceWrites(t, queries)
	})

	t.Run("unsupported MIME", func(t *testing.T) {
		queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
		files := &fakeFileStorage{objects: map[string]filestorage.ObjectAttrs{
			fileKey: {ContentType: "text/plain", Size: 12},
		}}
		response, _ := serveRegisterFileEvidence(&actor, queries, files, caseID, body)
		assertCaseTypeAPIError(t, response, http.StatusBadRequest, httpapi.CodeValidationError)
		assertNoEvidenceWrites(t, queries)
	})
}

func TestRegisterFileEvidenceRevalidatesAuthorization(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	caseID := handlerTestUUID(4).String()
	issueBody := `{"file_name":"evidence.pdf","mime_type":"application/pdf"}`

	for _, tt := range []struct {
		name   string
		mutate func(*fakeEvidenceTxQueries)
	}{
		{name: "state changed to submitted", mutate: func(q *fakeEvidenceTxQueries) { q.caseResult.Status = "SUBMITTED" }},
		{name: "participant removed", mutate: func(q *fakeEvidenceTxQueries) { q.participants = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
			files := &fakeFileStorage{objects: map[string]filestorage.ObjectAttrs{}}
			issued, _ := serveEvidenceUploadURL(&actor, queries, files, caseID, issueBody)
			if issued.Code != http.StatusOK {
				t.Fatalf("issue status = %d, want 200; body=%s", issued.Code, issued.Body.String())
			}
			var issuedBody struct {
				Data struct {
					FileKey string `json:"file_key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
				t.Fatal(err)
			}
			files.objects[issuedBody.Data.FileKey] = filestorage.ObjectAttrs{ContentType: "application/pdf", Size: 50}
			tt.mutate(queries)
			registerBody := `{"file_key":"` + issuedBody.Data.FileKey + `","title":"Evidence","evidence_type":"DOCUMENT"}`

			registered, _ := serveRegisterFileEvidence(&actor, queries, files, caseID, registerBody)

			assertCaseTypeAPIError(t, registered, http.StatusForbidden, httpapi.CodeForbidden)
			assertNoEvidenceWrites(t, queries)
			if files.statCalls != 0 {
				t.Errorf("stat calls = %d, want 0 when reauthorization fails", files.statCalls)
			}
		})
	}
}

func TestRegisterFileEvidenceStorageFailure(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := evidenceTestQueries(actor, "MAKER", "DRAFT")
	caseID := handlerTestUUID(4).String()
	fileKey := "cases/" + caseID + "/evidence/upload-id-evidence.png"
	files := &fakeFileStorage{statErr: errors.New("storage unavailable")}
	body := `{"file_key":"` + fileKey + `","title":"Evidence","evidence_type":"SCREENSHOT"}`

	response, _ := serveRegisterFileEvidence(&actor, queries, files, caseID, body)

	assertCaseTypeAPIError(t, response, http.StatusInternalServerError, httpapi.CodeInternalError)
	assertNoEvidenceWrites(t, queries)
}
