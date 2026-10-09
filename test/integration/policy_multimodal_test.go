//go:build integration

package integration

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/vertexai"
)

func TestPolicyLeaseAndEffectiveWindowGuards(t *testing.T) {
	admin := user(70, auth.SystemRoleAdmin)
	t.Run("stale indexing token cannot complete", func(t *testing.T) {
		store := newPolicyStore(db.PolicyVersion{ID: testUUID(72), PolicyID: testUUID(71), Version: "1",
			Status: "DRAFT", IndexStatus: "PROCESSING", IndexAttemptID: testUUID(73)})
		serve(t, handler.CompletePolicyVersionIndex(store), request(t, http.MethodPost, "/complete", map[string]any{
			"index_attempt_id": testUUID(74).String(), "index_status": "READY",
		}, admin, map[string]string{"id": testUUID(71).String(), "version_id": testUUID(72).String()}), http.StatusConflict)
		if store.version.IndexStatus != "PROCESSING" || !sameID(store.version.IndexAttemptID, testUUID(73)) || len(store.audits) != 0 {
			t.Fatalf("stale completion mutated state: version=%+v audits=%+v", store.version, store.audits)
		}
	})

	for _, tc := range []struct { name string; from, until pgtype.Timestamptz }{
		{name: "future effective", from: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
		{name: "expired", until: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPolicyStore(db.PolicyVersion{ID: testUUID(76), PolicyID: testUUID(75), Version: "2", Status: "DRAFT",
				IndexStatus: "READY", EffectiveFrom: tc.from, EffectiveUntil: tc.until})
			serve(t, handler.ActivatePolicyVersion(store), request(t, http.MethodPost, "/activate", nil, admin,
				map[string]string{"id": testUUID(75).String(), "version_id": testUUID(76).String()}), http.StatusConflict)
			if store.version.Status != "DRAFT" || len(store.audits) != 0 { t.Fatalf("invalid window activated: version=%+v audits=%+v", store.version, store.audits) }
		})
	}
}

func TestMultimodalEvidenceGCSURIsReachAIAdapter(t *testing.T) {
	caseID := testUUID(80)
	queries := &multimodalQueries{caseRow: db.Case{ID: caseID, CaseNumber: "CASE-80", CaseTypeID: testUUID(81), Title: "Multimodal",
		Status: "AI_ANALYSIS", CreatedBy: testUUID(82), OwnerID: testUUID(82), CreatedAt: epoch, UpdatedAt: epoch}, evidences: []db.CaseEvidence{
		{ID: testUUID(83), CaseID: caseID, EvidenceType: "DOCUMENT", FilePath: pgtype.Text{String: "cases/80/report.pdf", Valid: true}, MimeType: pgtype.Text{String: "application/pdf", Valid: true}},
		{ID: testUUID(84), CaseID: caseID, EvidenceType: "PHOTO", FilePath: pgtype.Text{String: "cases/80/photo.jpg", Valid: true}, MimeType: pgtype.Text{String: "image/jpeg", Valid: true}},
		{ID: testUUID(85), CaseID: caseID, EvidenceType: "PHOTO", FilePath: pgtype.Text{String: "cases/80/diagram.png", Valid: true}, MimeType: pgtype.Text{String: "image/png", Valid: true}},
	}}
	builder, err := ai.NewBuilder(queries, "sentinel-evidence", nil); if err != nil { t.Fatal(err) }
	built, err := builder.Build(context.Background(), caseID); if err != nil { t.Fatal(err) }
	prompt, err := ai.RenderPrompt(built); if err != nil { t.Fatal(err) }
	adapter := &assertingAIAdapter{t: t, want: []vertexai.FileInput{
		{URI: "gs://sentinel-evidence/cases/80/report.pdf", MIMEType: "application/pdf"},
		{URI: "gs://sentinel-evidence/cases/80/photo.jpg", MIMEType: "image/jpeg"},
		{URI: "gs://sentinel-evidence/cases/80/diagram.png", MIMEType: "image/png"},
	}}
	adapter.Generate(context.Background(), prompt.ToRequest())
	if adapter.calls != 1 { t.Fatalf("adapter calls=%d want=1", adapter.calls) }
}

type policyStore struct { version db.PolicyVersion; active *db.PolicyVersion; audits []db.AuditEvent }
func newPolicyStore(version db.PolicyVersion) *policyStore { return &policyStore{version: version} }
func (s *policyStore) RunPolicyActivationTx(ctx context.Context, fn func(context.Context, handler.PolicyActivationTxQueries) error) error { return fn(ctx, s) }
func (s *policyStore) GetPolicyVersionForUpdate(_ context.Context, id pgtype.UUID) (db.PolicyVersion, error) { if !sameID(id, s.version.ID) { return db.PolicyVersion{}, pgx.ErrNoRows }; return s.version, nil }
func (s *policyStore) GetActivePolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error) { if s.active == nil { return db.PolicyVersion{}, pgx.ErrNoRows }; return *s.active, nil }
func (s *policyStore) ClaimPolicyVersionIndex(_ context.Context, p db.ClaimPolicyVersionIndexParams) (db.PolicyVersion, error) {
	s.version.IndexStatus, s.version.IndexAttemptID = "PROCESSING", p.IndexAttemptID; return s.version, nil
}
func (s *policyStore) CompletePolicyVersionIndex(_ context.Context, p db.CompletePolicyVersionIndexParams) (db.PolicyVersion, error) {
	if !sameID(p.ID, s.version.ID) || !sameID(p.IndexAttemptID, s.version.IndexAttemptID) { return db.PolicyVersion{}, pgx.ErrNoRows }
	s.version.IndexStatus, s.version.IndexError = p.IndexStatus, p.IndexError; return s.version, nil
}
func (s *policyStore) UpdatePolicyVersionStatus(_ context.Context, p db.UpdatePolicyVersionStatusParams) (db.PolicyVersion, error) {
	if sameID(p.ID, s.version.ID) { s.version.Status = p.Status; return s.version, nil }
	if s.active != nil && sameID(p.ID, s.active.ID) { s.active.Status = p.Status; return *s.active, nil }
	return db.PolicyVersion{}, pgx.ErrNoRows
}
func (s *policyStore) AppendPolicyAuditEvent(_ context.Context, p db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	v := db.AuditEvent{ID: p.ID, ScopeType: "POLICY", PolicyID: p.PolicyID, PolicyVersionID: p.PolicyVersionID, EventType: p.EventType, ActorID: p.ActorID, Metadata: p.Metadata}; s.audits = append(s.audits, v); return v, nil
}

type multimodalQueries struct { caseRow db.Case; evidences []db.CaseEvidence }
func (q *multimodalQueries) GetCase(context.Context, pgtype.UUID) (db.Case, error) { return q.caseRow, nil }
func (q *multimodalQueries) ListCaseEvidences(context.Context, pgtype.UUID) ([]db.CaseEvidence, error) { return q.evidences, nil }
func (*multimodalQueries) ListActiveReadyPolicyChunks(context.Context, pgtype.UUID) ([]db.ListActiveReadyPolicyChunksRow, error) { return nil, nil }
func (*multimodalQueries) GetLatestAnalysisForCase(context.Context, pgtype.UUID) (db.AiAnalysis, error) { return db.AiAnalysis{}, pgx.ErrNoRows }
func (*multimodalQueries) ListDecisionsByAnalysis(context.Context, pgtype.UUID) ([]db.Decision, error) { return nil, nil }
func (*multimodalQueries) ListExecutionsForCase(context.Context, pgtype.UUID) ([]db.Execution, error) { return nil, nil }

type assertingAIAdapter struct { t *testing.T; want []vertexai.FileInput; calls int }
func (a *assertingAIAdapter) Generate(_ context.Context, request vertexai.Request) {
	a.t.Helper(); a.calls++
	if !reflect.DeepEqual(request.FileParts, a.want) { a.t.Fatalf("adapter file parts=%+v want=%+v", request.FileParts, a.want) }
}

var _ handler.PolicyActivationStore = (*policyStore)(nil)
var _ ai.ContextQueries = (*multimodalQueries)(nil)

