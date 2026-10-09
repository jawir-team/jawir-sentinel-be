//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/ai"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiconsumer"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/outboxdispatch"
	"github.com/jawir-team/jawir-sentinel-be/internal/rabbitmq"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
)

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testUUID(n byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{0: n, 6: 0x40, 8: 0x80, 15: n}, Valid: true}
}

func sameID(a, b pgtype.UUID) bool { return a.Valid && b.Valid && a.Bytes == b.Bytes }

type memoryStore struct {
	mu           sync.Mutex
	now          time.Time
	users        map[pgtype.UUID]db.User
	cases        map[pgtype.UUID]db.Case
	participants map[pgtype.UUID]db.CaseParticipant
	analyses     map[pgtype.UUID]db.AiAnalysis
	outboxes     map[pgtype.UUID]db.OutboxEvent
	decisions    map[pgtype.UUID]db.Decision
	executions   map[pgtype.UUID]db.Execution
	evidences    map[pgtype.UUID]db.CaseEvidence
	audits       []db.AuditEvent
	caseType     db.CaseType
	claimActive  bool
	finalizeRuns int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		now: epoch, users: map[pgtype.UUID]db.User{}, cases: map[pgtype.UUID]db.Case{},
		participants: map[pgtype.UUID]db.CaseParticipant{}, analyses: map[pgtype.UUID]db.AiAnalysis{},
		outboxes: map[pgtype.UUID]db.OutboxEvent{}, decisions: map[pgtype.UUID]db.Decision{},
		executions: map[pgtype.UUID]db.Execution{}, evidences: map[pgtype.UUID]db.CaseEvidence{},
		caseType: db.CaseType{ID: testUUID(90), Code: "OPS", Name: "Operations", CreatedAt: epoch, UpdatedAt: epoch},
	}
}

func (s *memoryStore) tick() time.Time { s.now = s.now.Add(time.Millisecond); return s.now }

func (s *memoryStore) CreateCase(_ context.Context, p db.CreateCaseParams) (db.Case, error) {
	c := db.Case{ID: p.ID, CaseNumber: p.CaseNumber, CaseTypeID: p.CaseTypeID, Title: p.Title,
		Description: p.Description, Urgency: p.Urgency, Status: "DRAFT", CreatedBy: p.CreatedBy,
		OwnerID: p.OwnerID, CreatedAt: s.tick(), UpdatedAt: s.now}
	s.cases[c.ID] = c
	return c, nil
}
func (s *memoryStore) GetCase(_ context.Context, id pgtype.UUID) (db.Case, error) {
	c, ok := s.cases[id]; if !ok { return db.Case{}, pgx.ErrNoRows }; return c, nil
}
func (s *memoryStore) GetCaseForUpdate(ctx context.Context, id pgtype.UUID) (db.Case, error) { return s.GetCase(ctx, id) }
func (s *memoryStore) ListCasesForUser(context.Context, db.ListCasesForUserParams) ([]db.Case, error) { return nil, nil }
func (s *memoryStore) UpdateCase(_ context.Context, p db.UpdateCaseParams) (db.Case, error) {
	c, ok := s.cases[p.ID]; if !ok { return db.Case{}, pgx.ErrNoRows }
	c.CaseTypeID, c.Title, c.Description, c.Urgency = p.CaseTypeID, p.Title, p.Description, p.Urgency
	c.UpdatedAt = s.tick(); s.cases[c.ID] = c; return c, nil
}
func (s *memoryStore) UpdateCaseStatus(_ context.Context, p db.UpdateCaseStatusParams) (db.Case, error) {
	c, ok := s.cases[p.ID]; if !ok { return db.Case{}, pgx.ErrNoRows }
	c.Status, c.UpdatedAt = p.Status, s.tick(); s.cases[c.ID] = c; return c, nil
}
func (s *memoryStore) IsActiveCaseParticipant(_ context.Context, p db.IsActiveCaseParticipantParams) (bool, error) {
	for _, v := range s.participants { if sameID(v.CaseID, p.CaseID) && sameID(v.UserID, p.UserID) && v.Status == "ACTIVE" { return true, nil } }
	return false, nil
}
func (s *memoryStore) CreateCaseParticipant(_ context.Context, p db.CreateCaseParticipantParams) (db.CaseParticipant, error) {
	v := db.CaseParticipant{ID: p.ID, CaseID: p.CaseID, UserID: p.UserID, Role: p.Role, Required: p.Required,
		Status: "ACTIVE", AssignedBy: p.AssignedBy, AssignedAt: s.tick()}
	s.participants[v.ID] = v; return v, nil
}
func (s *memoryStore) ListCaseParticipants(_ context.Context, caseID pgtype.UUID) ([]db.CaseParticipant, error) {
	var out []db.CaseParticipant
	for _, v := range s.participants { if sameID(v.CaseID, caseID) { out = append(out, v) } }
	sort.Slice(out, func(i, j int) bool { return out[i].AssignedAt.Before(out[j].AssignedAt) })
	return out, nil
}
func (s *memoryStore) GetUserForUpdate(_ context.Context, id pgtype.UUID) (db.User, error) {
	v, ok := s.users[id]; if !ok { return db.User{}, pgx.ErrNoRows }; return v, nil
}
func (s *memoryStore) GetParticipantByCaseUserRole(_ context.Context, p db.GetParticipantByCaseUserRoleParams) (db.CaseParticipant, error) {
	for _, v := range s.participants { if sameID(v.CaseID, p.CaseID) && sameID(v.UserID, p.UserID) && v.Role == p.Role { return v, nil } }
	return db.CaseParticipant{}, pgx.ErrNoRows
}
func (s *memoryStore) ReactivateCaseParticipant(_ context.Context, p db.ReactivateCaseParticipantParams) (db.CaseParticipant, error) {
	v, ok := s.participants[p.ID]; if !ok { return db.CaseParticipant{}, pgx.ErrNoRows }
	v.Status, v.AssignedBy, v.AssignedAt, v.UnassignedAt = "ACTIVE", p.AssignedBy, s.tick(), pgtype.Timestamptz{}
	s.participants[v.ID] = v; return v, nil
}
func (s *memoryStore) GetActiveParticipantForUpdate(_ context.Context, id pgtype.UUID) (db.CaseParticipant, error) {
	v, ok := s.participants[id]; if !ok || v.Status != "ACTIVE" { return db.CaseParticipant{}, pgx.ErrNoRows }; return v, nil
}
func (s *memoryStore) UnassignCaseParticipant(_ context.Context, id pgtype.UUID) (db.CaseParticipant, error) {
	v, ok := s.participants[id]; if !ok { return db.CaseParticipant{}, pgx.ErrNoRows }
	v.Status, v.UnassignedAt = "INACTIVE", pgtype.Timestamptz{Time: s.tick(), Valid: true}; s.participants[id] = v; return v, nil
}
func (s *memoryStore) CreateAnalysis(_ context.Context, p db.CreateAnalysisParams) (db.AiAnalysis, error) {
	v := db.AiAnalysis{ID: p.ID, CaseID: p.CaseID, Version: p.Version, Status: p.Status,
		ModelName: p.ModelName, PromptVersion: p.PromptVersion, CreatedAt: s.tick()}
	s.analyses[v.ID] = v; return v, nil
}
func (s *memoryStore) GetAnalysis(_ context.Context, id pgtype.UUID) (db.AiAnalysis, error) {
	v, ok := s.analyses[id]; if !ok { return db.AiAnalysis{}, pgx.ErrNoRows }; return v, nil
}
func (s *memoryStore) GetAnalysisForUpdate(ctx context.Context, id pgtype.UUID) (db.AiAnalysis, error) { return s.GetAnalysis(ctx, id) }
func (s *memoryStore) CreateOutboxEvent(_ context.Context, p db.CreateOutboxEventParams) (db.OutboxEvent, error) {
	v := db.OutboxEvent{ID: p.ID, CaseID: p.CaseID, AnalysisID: p.AnalysisID, EventType: p.EventType,
		Payload: append([]byte(nil), p.Payload...), Status: "PENDING", CreatedAt: s.tick(), UpdatedAt: s.now}
	s.outboxes[v.ID] = v; return v, nil
}
func (s *memoryStore) GetOutboxEvent(_ context.Context, id pgtype.UUID) (db.OutboxEvent, error) {
	v, ok := s.outboxes[id]; if !ok { return db.OutboxEvent{}, pgx.ErrNoRows }; return v, nil
}
func (s *memoryStore) AppendCaseAuditEvent(_ context.Context, p db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	v := db.AuditEvent{ID: p.ID, ScopeType: "CASE", CaseID: p.CaseID, EventType: p.EventType,
		ActorID: p.ActorID, ActorRole: p.ActorRole, AnalysisID: p.AnalysisID,
		Metadata: append([]byte(nil), p.Metadata...), CreatedAt: s.tick()}
	s.audits = append(s.audits, v); return v, nil
}
func (s *memoryStore) AppendPolicyAuditEvent(_ context.Context, p db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	v := db.AuditEvent{ID: p.ID, ScopeType: "POLICY", PolicyID: p.PolicyID, PolicyVersionID: p.PolicyVersionID,
		EventType: p.EventType, ActorID: p.ActorID, Metadata: append([]byte(nil), p.Metadata...), CreatedAt: s.tick()}
	s.audits = append(s.audits, v); return v, nil
}
func (s *memoryStore) ListCaseAuditEvents(_ context.Context, caseID pgtype.UUID) ([]db.AuditEvent, error) {
	var out []db.AuditEvent; for _, v := range s.audits { if sameID(v.CaseID, caseID) { out = append(out, v) } }; return out, nil
}
func (s *memoryStore) RunParticipantTx(ctx context.Context, fn func(context.Context, handler.ParticipantTxQueries) error) error { return fn(ctx, s) }
func (s *memoryStore) RunSubmitTx(ctx context.Context, fn func(context.Context, handler.SubmitTxQueries) error) error { return fn(ctx, s) }
func (s *memoryStore) RunCheckerDecisionTx(ctx context.Context, fn func(context.Context, handler.CheckerDecisionTxQueries) error) error { return fn(ctx, s) }
func (s *memoryStore) RunExecutionTx(ctx context.Context, fn func(context.Context, handler.ExecutionTxQueries) error) error { return fn(ctx, s) }
func (s *memoryStore) RunCloseTx(ctx context.Context, fn func(context.Context, handler.CloseTxQueries) error) error { return fn(ctx, s) }
func (s *memoryStore) Run(ctx context.Context, fn func(context.Context, aiworker.StateQueries) error) error { return fn(ctx, s) }

func (s *memoryStore) IsAnalysisClaimActive(context.Context, db.IsAnalysisClaimActiveParams) (bool, error) { return s.claimActive, nil }
func (s *memoryStore) ClaimAnalysis(_ context.Context, p db.ClaimAnalysisParams) (db.AiAnalysis, error) {
	v, ok := s.analyses[p.ID]; if !ok || !sameID(v.CaseID, p.CaseID) { return db.AiAnalysis{}, pgx.ErrNoRows }
	v.WorkerAttemptID = p.WorkerAttemptID; v.WorkerStartedAt = pgtype.Timestamptz{Time: s.tick(), Valid: true}
	s.analyses[v.ID], s.claimActive = v, true; return v, nil
}
func (s *memoryStore) RetryAnalysis(_ context.Context, analysisID, attemptID pgtype.UUID) (db.AiAnalysis, error) {
	v := s.analyses[analysisID]; if !sameID(v.WorkerAttemptID, attemptID) { return db.AiAnalysis{}, aiworker.ErrStaleClaim }
	v.TechnicalRetryCount++; v.WorkerAttemptID = testUUID(byte(150 + v.TechnicalRetryCount)); v.WorkerStartedAt = pgtype.Timestamptz{Time: s.tick(), Valid: true}
	s.analyses[v.ID] = v; return v, nil
}
func (s *memoryStore) FinalizeCompleted(_ context.Context, analysisID, attemptID pgtype.UUID, result aiworker.SuccessResult) (db.AiAnalysis, error) {
	v := s.analyses[analysisID]; if !sameID(v.WorkerAttemptID, attemptID) { return db.AiAnalysis{}, aiworker.ErrStaleClaim }
	v.Status, v.Summary = "COMPLETED", pgtype.Text{String: result.Candidate.Summary, Valid: true}
	v.Facts, _ = json.Marshal(result.Candidate.Facts); v.Assumptions, _ = json.Marshal(result.Candidate.Assumptions)
	v.Unknowns, _ = json.Marshal(result.Candidate.Unknowns); v.RiskAnalysis, _ = json.Marshal(result.Candidate.RiskAnalysis)
	v.ComplianceAnalysis, _ = json.Marshal(result.Candidate.ComplianceAnalysis); v.Recommendation, _ = json.Marshal(result.Candidate.Recommendation)
	v.Alternatives, _ = json.Marshal(result.Candidate.Alternatives); v.MissingInformation, _ = json.Marshal(result.Candidate.MissingInformation)
	v.PolicyStatus = pgtype.Text{String: string(result.Candidate.PolicyStatus), Valid: true}
	v.EvidenceQuality = pgtype.Text{String: string(result.Candidate.EvidenceQuality), Valid: true}
	v.Uncertainty = pgtype.Text{String: string(result.Candidate.Uncertainty), Valid: true}
	v.VerificationStatus = pgtype.Text{String: string(result.Verification.Status), Valid: true}
	v.VerificationNotes, _ = result.Verification.VerificationNotesJSON()
	s.analyses[v.ID] = v; c := s.cases[v.CaseID]; c.CurrentAnalysisID = v.ID; s.cases[c.ID] = c; s.finalizeRuns++; return v, nil
}
func (s *memoryStore) FinalizeFailed(_ context.Context, analysisID, attemptID pgtype.UUID, failed *aiworker.FailedCandidate) (db.AiAnalysis, error) {
	v := s.analyses[analysisID]; if !sameID(v.WorkerAttemptID, attemptID) { return db.AiAnalysis{}, aiworker.ErrStaleClaim }
	v.Status = "FAILED"
	if failed != nil { v.Summary = pgtype.Text{String: failed.Candidate.Summary, Valid: true}; v.VerificationStatus = pgtype.Text{String: string(failed.Verification.Status), Valid: true}; v.VerificationNotes, _ = failed.Verification.VerificationNotesJSON() }
	s.analyses[v.ID] = v; s.finalizeRuns++; return v, nil
}

func (s *memoryStore) CreateDecision(_ context.Context, p db.CreateDecisionParams) (db.Decision, error) {
	v := db.Decision{ID: p.ID, CaseID: p.CaseID, AnalysisID: p.AnalysisID, ActorID: p.ActorID, ActorRole: p.ActorRole,
		Decision: p.Decision, Reason: p.Reason, Comment: p.Comment, CreatedAt: s.tick()}; s.decisions[v.ID] = v; return v, nil
}
func (s *memoryStore) ListDecisionsByAnalysis(_ context.Context, analysisID pgtype.UUID) ([]db.Decision, error) {
	var out []db.Decision; for _, v := range s.decisions { if sameID(v.AnalysisID, analysisID) { out = append(out, v) } }
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) }); return out, nil
}
func (s *memoryStore) GetCaseType(context.Context, pgtype.UUID) (db.CaseType, error) { return s.caseType, nil }
func (s *memoryStore) ListAnalysisPolicyRefs(context.Context, pgtype.UUID) ([]db.AnalysisPolicyRef, error) { return []db.AnalysisPolicyRef{}, nil }
func (s *memoryStore) ListAnalysisEvidenceRefs(context.Context, pgtype.UUID) ([]db.AnalysisEvidenceRef, error) { return []db.AnalysisEvidenceRef{}, nil }
func (s *memoryStore) GetPolicyVersion(context.Context, pgtype.UUID) (db.PolicyVersion, error) { return db.PolicyVersion{}, pgx.ErrNoRows }
func (s *memoryStore) CreateEvidence(_ context.Context, p db.CreateEvidenceParams) (db.CaseEvidence, error) {
	v := db.CaseEvidence{ID: p.ID, CaseID: p.CaseID, SourceType: p.SourceType, SourceUserID: p.SourceUserID,
		EvidenceType: p.EvidenceType, Title: p.Title, Content: p.Content, FilePath: p.FilePath, MimeType: p.MimeType, CreatedAt: s.tick()}
	s.evidences[v.ID] = v; return v, nil
}
func (s *memoryStore) ListCaseEvidences(_ context.Context, caseID pgtype.UUID) ([]db.CaseEvidence, error) {
	var out []db.CaseEvidence; for _, v := range s.evidences { if sameID(v.CaseID, caseID) { out = append(out, v) } }; return out, nil
}

func (s *memoryStore) ExistsExecutionForCaseAnalysis(_ context.Context, p db.ExistsExecutionForCaseAnalysisParams) (bool, error) {
	for _, v := range s.executions { if sameID(v.CaseID, p.CaseID) && sameID(v.AnalysisID, p.AnalysisID) { return true, nil } }; return false, nil
}
func (s *memoryStore) CreateExecution(_ context.Context, p db.CreateExecutionParams) (db.Execution, error) {
	v := db.Execution{ID: p.ID, CaseID: p.CaseID, AnalysisID: p.AnalysisID, ExecuterID: p.ExecuterID,
		Status: p.Status, StartedAt: s.tick(), CreatedAt: s.now}; s.executions[v.ID] = v; return v, nil
}
func (s *memoryStore) GetExecutionForUpdate(_ context.Context, id pgtype.UUID) (db.Execution, error) {
	v, ok := s.executions[id]; if !ok { return db.Execution{}, pgx.ErrNoRows }; return v, nil
}
func (s *memoryStore) UpdateExecution(_ context.Context, p db.UpdateExecutionParams) (db.Execution, error) {
	v, ok := s.executions[p.ID]; if !ok { return db.Execution{}, pgx.ErrNoRows }
	v.Status, v.ActionTaken, v.Result, v.Blocker, v.CompletedAt = p.Status, p.ActionTaken, p.Result, p.Blocker, p.CompletedAt
	s.executions[v.ID] = v; return v, nil
}
func (s *memoryStore) ExistsGeneratingAnalysis(_ context.Context, caseID pgtype.UUID) (bool, error) {
	for _, v := range s.analyses { if sameID(v.CaseID, caseID) && v.Status == "GENERATING" { return true, nil } }; return false, nil
}
func (s *memoryStore) ExistsRunningExecution(_ context.Context, caseID pgtype.UUID) (bool, error) {
	for _, v := range s.executions { if sameID(v.CaseID, caseID) && v.Status == "IN_PROGRESS" { return true, nil } }; return false, nil
}
func (s *memoryStore) CloseCase(_ context.Context, p db.CloseCaseParams) (db.Case, error) {
	v, ok := s.cases[p.ID]; if !ok { return db.Case{}, pgx.ErrNoRows }
	v.Status, v.ClosedBy, v.CloseReason, v.ClosedAt = "CLOSED", p.ClosedBy, pgtype.Text{String: p.CloseReason, Valid: true}, pgtype.Timestamptz{Time: s.tick(), Valid: true}
	s.cases[v.ID] = v; return v, nil
}

type memoryOutboxFactory struct{ store *memoryStore }
func (f memoryOutboxFactory) Begin(context.Context) (outboxdispatch.TxStore, error) { return &memoryOutboxTx{store: f.store}, nil }
type memoryOutboxTx struct { store *memoryStore; done bool }
func (t *memoryOutboxTx) ListPending(context.Context, int32) ([]db.OutboxEvent, error) {
	var out []db.OutboxEvent; for _, v := range t.store.outboxes { if v.Status == "PENDING" { out = append(out, v) } }
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) }); return out, nil
}
func (t *memoryOutboxTx) MarkPublished(_ context.Context, id pgtype.UUID, at time.Time) error {
	v := t.store.outboxes[id]; v.Status, v.PublishedAt, v.LastError, v.UpdatedAt = "PUBLISHED", pgtype.Timestamptz{Time: at, Valid: true}, pgtype.Text{}, at; t.store.outboxes[id] = v; return nil
}
func (t *memoryOutboxTx) RecordAttempt(_ context.Context, id pgtype.UUID, message string) error {
	v := t.store.outboxes[id]; v.AttemptCount++; v.LastError = pgtype.Text{String: message, Valid: true}; t.store.outboxes[id] = v; return nil
}
func (t *memoryOutboxTx) Commit(context.Context) error { t.done = true; return nil }
func (t *memoryOutboxTx) Rollback(context.Context) error { return nil }

type capturePublisher struct { mu sync.Mutex; failures int; calls int; messages []rabbitmq.Message }
func (p *capturePublisher) Publish(_ context.Context, id string, body []byte) error {
	p.mu.Lock(); defer p.mu.Unlock(); p.calls++
	if p.failures > 0 { p.failures--; return errors.New("broker unavailable") }
	p.messages = append(p.messages, rabbitmq.Message{MessageID: id, Body: append([]byte(nil), body...)}); return nil
}

type staticExecutor struct { execution aiconsumer.Execution; calls int }
func (e *staticExecutor) Execute(context.Context, aiconsumer.Job) (aiconsumer.Execution, error) { e.calls++; return e.execution, nil }

func validCandidate() ai.CandidateAnalysis {
	c := ai.NewCandidateAnalysis(); c.Summary = "Controls are satisfied"; c.PolicyStatus = ai.PolicyStatusFound
	c.EvidenceQuality, c.Uncertainty = ai.QualityHigh, ai.QualityLow
	c.ComplianceAnalysis.Status, c.ComplianceAnalysis.Reason = ai.ComplianceNoIssueIdentified, "No issue identified"
	c.Recommendation.Type, c.Recommendation.Summary = ai.RecommendationTypePolicyBased, "Proceed"
	return c
}
func successExecution() aiconsumer.Execution {
	return aiconsumer.Execution{Success: &aiworker.SuccessResult{Candidate: validCandidate(), Verification: ai.VerificationResult{Status: ai.VerificationStatusPass, Issues: []ai.VerifierIssue{}}}}
}

func request(t *testing.T, method, target string, body any, actor auth.User, params map[string]string) *http.Request {
	t.Helper(); var raw []byte
	if body != nil { var err error; raw, err = json.Marshal(body); if err != nil { t.Fatal(err) } }
	r := httptest.NewRequest(method, target, bytes.NewReader(raw)); r.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext(); for k, v := range params { rctx.URLParams.Add(k, v) }
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx); ctx = auth.WithUser(ctx, actor); return r.WithContext(ctx)
}
func serve(t *testing.T, h http.Handler, req *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper(); rec := httptest.NewRecorder(); h.ServeHTTP(rec, req)
	if rec.Code != want { t.Fatalf("%s %s: status=%d want=%d body=%s", req.Method, req.URL.Path, rec.Code, want, rec.Body.String()) }; return rec
}
func oneCase(t *testing.T, s *memoryStore) db.Case { t.Helper(); if len(s.cases) != 1 { t.Fatalf("cases=%d want=1", len(s.cases)) }; for _, v := range s.cases { return v }; panic("unreachable") }
func oneAnalysis(t *testing.T, s *memoryStore) db.AiAnalysis { t.Helper(); if len(s.analyses) != 1 { t.Fatalf("analyses=%d want=1", len(s.analyses)) }; for _, v := range s.analyses { return v }; panic("unreachable") }
func oneOutbox(t *testing.T, s *memoryStore) db.OutboxEvent { t.Helper(); if len(s.outboxes) != 1 { t.Fatalf("outboxes=%d want=1", len(s.outboxes)) }; for _, v := range s.outboxes { return v }; panic("unreachable") }
func oneExecution(t *testing.T, s *memoryStore) db.Execution { t.Helper(); if len(s.executions) != 1 { t.Fatalf("executions=%d want=1", len(s.executions)) }; for _, v := range s.executions { return v }; panic("unreachable") }

func user(id byte, role string) auth.User { return auth.User{ID: testUUID(id), Name: fmt.Sprintf("user-%d", id), Email: fmt.Sprintf("u%d@example.test", id), SystemRole: role} }
func seedUsers(s *memoryStore, users ...auth.User) { for _, u := range users { s.users[u.ID] = db.User{ID: u.ID, Name: u.Name, Email: u.Email, Status: "ACTIVE", SystemRole: u.SystemRole} } }

var (
	_ handler.CaseStore = (*memoryStore)(nil)
	_ handler.CaseParticipantStore = (*memoryStore)(nil)
	_ handler.SubmitCaseStore = (*memoryStore)(nil)
	_ handler.CheckerDecisionStore = (*memoryStore)(nil)
	_ handler.ExecutionStore = (*memoryStore)(nil)
	_ handler.CloseCaseStore = (*memoryStore)(nil)
	_ handler.HistoryStore = (*memoryStore)(nil)
	_ aiworker.StateStore = (*memoryStore)(nil)
	_ outboxdispatch.TxFactory = memoryOutboxFactory{}
	_ outboxdispatch.Publisher = (*capturePublisher)(nil)
	_ aiconsumer.Executor = (*staticExecutor)(nil)
	_ reanalysis.TransactionRunner = (*quotaRunner)(nil)
)

