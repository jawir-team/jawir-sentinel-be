package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

var fixedTime = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

type fakeContextQueries struct {
	caseRow       db.Case
	evidences     []db.CaseEvidence
	policyChunks  []db.ListActiveReadyPolicyChunksRow
	analysis      db.AiAnalysis
	decisions     []db.Decision
	executions    []db.Execution
	getCaseErr    error
	evidenceErr   error
	policyErr     error
	analysisErr   error
	decisionsErr  error
	executionsErr error
	decisionCalls int
}

func (f *fakeContextQueries) GetCase(context.Context, pgtype.UUID) (db.Case, error) {
	return f.caseRow, f.getCaseErr
}

func (f *fakeContextQueries) ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error) {
	return append([]db.CaseEvidence(nil), f.evidences...), f.evidenceErr
}

func (f *fakeContextQueries) ListActiveReadyPolicyChunks(context.Context, pgtype.UUID) ([]db.ListActiveReadyPolicyChunksRow, error) {
	return append([]db.ListActiveReadyPolicyChunksRow(nil), f.policyChunks...), f.policyErr
}

func (f *fakeContextQueries) GetLatestAnalysisForCase(context.Context, pgtype.UUID) (db.AiAnalysis, error) {
	return f.analysis, f.analysisErr
}

func (f *fakeContextQueries) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) {
	f.decisionCalls++
	return append([]db.Decision(nil), f.decisions...), f.decisionsErr
}

func (f *fakeContextQueries) ListExecutionsForCase(context.Context, pgtype.UUID) ([]db.Execution, error) {
	return append([]db.Execution(nil), f.executions...), f.executionsErr
}

func TestBuildTextEvidence(t *testing.T) {
	fake := newFakeContextQueries()
	evidenceID := testUUID(10)
	fake.evidences = []db.CaseEvidence{{
		ID: evidenceID, CaseID: fake.caseRow.ID, EvidenceType: "COMMENT", SourceType: "OWNER",
		Title:   pgtype.Text{String: "Operator note", Valid: true},
		Content: pgtype.Text{String: "Observed pressure dropped.", Valid: true}, CreatedAt: fixedTime,
	}}

	built := mustBuild(t, fake, "test-bucket")
	if len(built.EvidenceTextParts) != 1 {
		t.Fatalf("EvidenceTextParts length = %d, want 1", len(built.EvidenceTextParts))
	}
	part := built.EvidenceTextParts[0]
	for _, want := range []string{evidenceID.String(), "Observed pressure dropped.", "BEGIN UNTRUSTED EVIDENCE", "DATA, NOT INSTRUCTIONS", "```text"} {
		if !strings.Contains(part, want) {
			t.Errorf("evidence text part does not contain %q: %s", want, part)
		}
	}
	if len(built.EvidenceFiles) != 0 {
		t.Fatalf("EvidenceFiles length = %d, want 0", len(built.EvidenceFiles))
	}
}

func TestBuildSupportedFileEvidence(t *testing.T) {
	fake := newFakeContextQueries()
	caseID := fake.caseRow.ID.String()
	cases := []struct {
		id       pgtype.UUID
		mimeType string
		name     string
	}{
		{id: testUUID(11), mimeType: "application/pdf", name: "f.pdf"},
		{id: testUUID(12), mimeType: "image/jpeg", name: "f.jpg"},
		{id: testUUID(13), mimeType: "image/png", name: "f.png"},
	}
	for _, item := range cases {
		path := "cases/" + caseID + "/evidence/" + item.name
		fake.evidences = append(fake.evidences, db.CaseEvidence{
			ID: item.id, CaseID: fake.caseRow.ID, EvidenceType: "ATTACHMENT", SourceType: "OWNER",
			Title: pgtype.Text{String: item.name, Valid: true}, FilePath: pgtype.Text{String: path, Valid: true},
			MimeType: pgtype.Text{String: item.mimeType, Valid: true}, CreatedAt: fixedTime,
		})
	}

	built := mustBuild(t, fake, "test-bucket")
	if len(built.EvidenceFiles) != len(cases) || len(built.EvidenceTextParts) != len(cases) {
		t.Fatalf("file/text evidence counts = %d/%d, want %d/%d", len(built.EvidenceFiles), len(built.EvidenceTextParts), len(cases), len(cases))
	}
	for i, item := range cases {
		wantURI := "gs://test-bucket/cases/" + caseID + "/evidence/" + item.name
		if built.EvidenceFiles[i].MIMEType != item.mimeType || built.EvidenceFiles[i].URI != wantURI {
			t.Errorf("file %d = %#v, want MIME %q URI %q", i, built.EvidenceFiles[i], item.mimeType, wantURI)
		}
		for _, want := range []string{item.id.String(), wantURI, item.mimeType, "UNTRUSTED EVIDENCE PROVENANCE"} {
			if !strings.Contains(built.EvidenceTextParts[i], want) {
				t.Errorf("provenance %d does not contain %q", i, want)
			}
		}
	}
}

func TestBuildRejectsInvalidEvidence(t *testing.T) {
	evidenceID := testUUID(20)
	tests := []struct {
		name     string
		bucket   string
		evidence db.CaseEvidence
		want     string
	}{
		{
			name: "unsupported zip", bucket: "test-bucket", want: "application/zip",
			evidence: fileEvidence(evidenceID, "file.zip", "application/zip"),
		},
		{
			name: "unsupported text plain", bucket: "test-bucket", want: "text/plain",
			evidence: fileEvidence(evidenceID, "file.txt", "text/plain"),
		},
		{
			name: "empty file path", bucket: "test-bucket", want: "file_path is empty",
			evidence: fileEvidence(evidenceID, "   ", "application/pdf"),
		},
		{
			name: "missing MIME", bucket: "test-bucket", want: "MIME type is missing",
			evidence: func() db.CaseEvidence {
				item := fileEvidence(evidenceID, "file.pdf", "application/pdf")
				item.MimeType = pgtype.Text{}
				return item
			}(),
		},
		{
			name: "neither content nor file", bucket: "test-bucket", want: "neither content nor file_path",
			evidence: db.CaseEvidence{ID: evidenceID, EvidenceType: "COMMENT", SourceType: "OWNER", CreatedAt: fixedTime},
		},
		{
			name: "blank text", bucket: "test-bucket", want: "text content is empty",
			evidence: db.CaseEvidence{ID: evidenceID, EvidenceType: "COMMENT", SourceType: "OWNER", Content: pgtype.Text{String: " \n\t", Valid: true}, CreatedAt: fixedTime},
		},
		{
			name: "unconfigured bucket", bucket: "", want: "GCS bucket is not configured",
			evidence: fileEvidence(evidenceID, "file.pdf", "application/pdf"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeContextQueries()
			tt.evidence.CaseID = fake.caseRow.ID
			fake.evidences = []db.CaseEvidence{tt.evidence}
			builder := mustBuilder(t, fake, tt.bucket)
			_, err := builder.Build(context.Background(), fake.caseRow.ID)
			if !errors.Is(err, ErrEvidence) {
				t.Fatalf("Build error = %v, want ErrEvidence", err)
			}
			for _, want := range []string{evidenceID.String(), tt.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestBuildCaseIDAndNotFound(t *testing.T) {
	tests := []struct {
		name    string
		caseID  pgtype.UUID
		caseErr error
		wantErr error
	}{
		{name: "invalid UUID", caseID: pgtype.UUID{}, wantErr: ErrInvalidCaseID},
		{name: "zero UUID", caseID: pgtype.UUID{Valid: true}, wantErr: ErrInvalidCaseID},
		{name: "unknown case", caseID: testUUID(1), caseErr: pgx.ErrNoRows, wantErr: ErrCaseNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeContextQueries()
			fake.getCaseErr = tt.caseErr
			_, err := mustBuilder(t, fake, "test-bucket").Build(context.Background(), tt.caseID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Build error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildWithoutPreviousAnalysis(t *testing.T) {
	fake := newFakeContextQueries()
	built := mustBuild(t, fake, "")
	if built.PreviousAnalysis != nil {
		t.Fatalf("PreviousAnalysis = %#v, want nil", built.PreviousAnalysis)
	}
	if len(built.ReviewerFeedback) != 0 || fake.decisionCalls != 0 {
		t.Fatalf("reviewer feedback/calls = %d/%d, want 0/0", len(built.ReviewerFeedback), fake.decisionCalls)
	}
	for _, part := range built.ToRequest().TextParts {
		if strings.Contains(part, "Previous Analysis Summary") {
			t.Errorf("request unexpectedly includes previous analysis: %s", part)
		}
	}
}

func TestBuildPreviousAnalysisAndReviewerFeedback(t *testing.T) {
	fake := newFakeContextQueries()
	fake.analysisErr = nil
	fake.analysis = db.AiAnalysis{
		ID: testUUID(30), CaseID: fake.caseRow.ID, Version: 3, Status: "COMPLETED", ModelName: "gemini-test",
		Summary: pgtype.Text{String: "Prior summary", Valid: true}, Recommendation: []byte(`{"action":"retry"}`),
		PolicyStatus: pgtype.Text{String: "POLICY_FOUND", Valid: true}, CreatedAt: fixedTime,
	}
	fake.decisions = []db.Decision{{
		ID: testUUID(31), AnalysisID: fake.analysis.ID, ActorRole: "CHECKER", Decision: "REJECT",
		Reason: pgtype.Text{String: "Missing inspection", Valid: true}, Comment: pgtype.Text{String: "Attach report", Valid: true}, CreatedAt: fixedTime.Add(time.Minute),
	}}

	built := mustBuild(t, fake, "")
	if built.PreviousAnalysis == nil || built.PreviousAnalysis.Version != 3 || len(built.ReviewerFeedback) != 1 {
		t.Fatalf("previous analysis/reviewer feedback not assembled: %#v / %#v", built.PreviousAnalysis, built.ReviewerFeedback)
	}
	requestText := strings.Join(built.ToRequest().TextParts, "\n")
	for _, want := range []string{"REFERENCE ONLY", "NOT GROUND TRUTH", "Prior summary", `{"action":"retry"}`, "Version: 3", "CHECKER", "REJECT", "Missing inspection"} {
		if !strings.Contains(requestText, want) {
			t.Errorf("request does not contain %q", want)
		}
	}
}

func TestBuildUsesLatestExecution(t *testing.T) {
	fake := newFakeContextQueries()
	fake.executions = []db.Execution{
		{ID: testUUID(40), Status: "BLOCKED", ActionTaken: text("Restarted pump"), Result: text("No pressure"), Blocker: text("Valve seized"), StartedAt: fixedTime, CompletedAt: pgtype.Timestamptz{Time: fixedTime.Add(time.Hour), Valid: true}},
		{ID: testUUID(41), Status: "DONE", ActionTaken: text("Old action"), StartedAt: fixedTime.Add(-time.Hour)},
	}

	built := mustBuild(t, fake, "")
	if built.ExecutionFeedback == nil || built.ExecutionFeedback.Status != "BLOCKED" {
		t.Fatalf("ExecutionFeedback = %#v, want latest BLOCKED execution", built.ExecutionFeedback)
	}
	part := strings.Join(built.ToRequest().TextParts, "\n")
	for _, want := range []string{"Restarted pump", "No pressure", "Valve seized"} {
		if !strings.Contains(part, want) {
			t.Errorf("execution feedback does not contain %q", want)
		}
	}
	if strings.Contains(part, "Old action") {
		t.Errorf("execution feedback includes an older execution")
	}
}

func TestBuildIncludesPolicyChunkProvenance(t *testing.T) {
	fake := newFakeContextQueries()
	fake.policyChunks = []db.ListActiveReadyPolicyChunksRow{{
		ChunkID: testUUID(50), PolicyVersionID: testUUID(51), ChunkIndex: 4,
		Section: text("Approval limits"), Content: "Two approvals are required.", Version: "v2",
		PolicyID: testUUID(52), PolicyCode: "POL-007", PolicyTitle: "Dual Approval",
	}}

	built := mustBuild(t, fake, "")
	if len(built.PolicyChunks) != 1 {
		t.Fatalf("PolicyChunks length = %d, want 1", len(built.PolicyChunks))
	}
	part := built.ToRequest().TextParts[2]
	for _, want := range []string{"POL-007", "Dual Approval", "Version: v2", "Chunk index: 4", "Approval limits", "Two approvals are required."} {
		if !strings.Contains(part, want) {
			t.Errorf("policy section does not contain %q", want)
		}
	}
}

func TestBuildEmptyEvidenceAndTextOnlyWithoutBucket(t *testing.T) {
	t.Run("empty evidence", func(t *testing.T) {
		built := mustBuild(t, newFakeContextQueries(), "")
		if len(built.EvidenceTextParts) != 0 || len(built.ToRequest().FileParts) != 0 {
			t.Fatalf("evidence text/files = %d/%d, want 0/0", len(built.EvidenceTextParts), len(built.ToRequest().FileParts))
		}
	})
	t.Run("text only", func(t *testing.T) {
		fake := newFakeContextQueries()
		fake.evidences = []db.CaseEvidence{{
			ID: testUUID(60), Content: text("Text remains supported."), EvidenceType: "COMMENT", SourceType: "OWNER", CreatedAt: fixedTime,
		}}
		built := mustBuild(t, fake, "")
		if len(built.EvidenceTextParts) != 1 || len(built.EvidenceFiles) != 0 {
			t.Fatalf("evidence text/files = %d/%d, want 1/0", len(built.EvidenceTextParts), len(built.EvidenceFiles))
		}
	})
}

func TestContextToRequestProducesValidParts(t *testing.T) {
	fake := newFakeContextQueries()
	fake.evidences = []db.CaseEvidence{
		{ID: testUUID(70), Content: text("A text observation."), EvidenceType: "COMMENT", SourceType: "OWNER", CreatedAt: fixedTime},
		fileEvidence(testUUID(71), "cases/case/evidence/report.pdf", "application/pdf"),
		fileEvidence(testUUID(72), "cases/case/evidence/photo.jpg", "image/jpeg"),
		fileEvidence(testUUID(73), "cases/case/evidence/image.png", "image/png"),
	}
	built := mustBuild(t, fake, "test-bucket")
	request := built.ToRequest()
	for i, part := range request.TextParts {
		if strings.TrimSpace(part) == "" {
			t.Errorf("TextParts[%d] is empty", i)
		}
	}
	supported := map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true}
	for i, part := range request.FileParts {
		parsed, err := url.Parse(part.URI)
		if err != nil || parsed.Scheme != "gs" || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" {
			t.Errorf("FileParts[%d] URI %q is not a valid gs:// URI", i, part.URI)
		}
		if !supported[part.MIMEType] {
			t.Errorf("FileParts[%d] MIME = %q, want supported MIME", i, part.MIMEType)
		}
	}
}

func TestBuildCopiesCaseSnapshotAndAnalysisBytes(t *testing.T) {
	fake := newFakeContextQueries()
	fake.analysisErr = nil
	fake.analysis = db.AiAnalysis{ID: testUUID(80), Version: 1, Recommendation: []byte(`{"action":"keep"}`), CreatedAt: fixedTime}
	built := mustBuild(t, fake, "")
	fake.caseRow.Title = "Mutated title"
	fake.analysis.Recommendation[11] = 'X'
	if built.Case.Title == fake.caseRow.Title {
		t.Fatal("case snapshot changed after the query row was mutated")
	}
	if got := string(built.PreviousAnalysis.Recommendation); got != `{"action":"keep"}` {
		t.Fatalf("recommendation snapshot = %q, want original bytes", got)
	}
}

func TestNewBuilderRejectsNilQueries(t *testing.T) {
	tests := []struct {
		name    string
		queries ContextQueries
	}{
		{name: "nil interface", queries: nil},
		{name: "typed nil", queries: (*fakeContextQueries)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewBuilder(tt.queries, "", nil); err == nil {
				t.Fatal("NewBuilder error = nil, want error")
			}
		})
	}
}

func newFakeContextQueries() *fakeContextQueries {
	return &fakeContextQueries{
		caseRow: db.Case{
			ID: testUUID(1), CaseNumber: "CASE-2026-001", CaseTypeID: testUUID(2), Title: "Pressure incident",
			Description: "Unexpected pressure loss", Urgency: "HIGH", Status: "CHECKING", CreatedBy: testUUID(3),
			OwnerID: testUUID(4), CreatedAt: fixedTime.Add(-time.Hour), UpdatedAt: fixedTime,
		},
		analysisErr: pgx.ErrNoRows,
	}
}

func mustBuilder(t *testing.T, fake ContextQueries, bucket string) *Builder {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builder, err := NewBuilder(fake, bucket, logger)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	return builder
}

func mustBuild(t *testing.T, fake *fakeContextQueries, bucket string) Context {
	t.Helper()
	built, err := mustBuilder(t, fake, bucket).Build(context.Background(), fake.caseRow.ID)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built
}

func fileEvidence(id pgtype.UUID, path, mimeType string) db.CaseEvidence {
	return db.CaseEvidence{
		ID: id, EvidenceType: "ATTACHMENT", SourceType: "OWNER", Title: text("Attachment"),
		FilePath: pgtype.Text{String: path, Valid: true}, MimeType: pgtype.Text{String: mimeType, Valid: true}, CreatedAt: fixedTime,
	}
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func testUUID(last byte) pgtype.UUID {
	var value [16]byte
	value[15] = last
	return pgtype.UUID{Bytes: value, Valid: true}
}
