package audit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

type fakeQueries struct {
	analyses       map[pgtype.UUID]db.AiAnalysis
	policyVersions map[pgtype.UUID]db.PolicyVersion
	caseArgs       []db.AppendCaseAuditEventParams
	policyArgs     []db.AppendPolicyAuditEventParams
}

var _ Queries = (*fakeQueries)(nil)

// This compile-time contract intentionally exposes only append operations as
// mutations. The scoped audit service has no update or delete capability.
type appendOnlyMutations interface {
	AppendCaseAuditEvent(context.Context, db.AppendCaseAuditEventParams) (db.AuditEvent, error)
	AppendPolicyAuditEvent(context.Context, db.AppendPolicyAuditEventParams) (db.AuditEvent, error)
}

var _ appendOnlyMutations = (Queries)(nil)

func (f *fakeQueries) AppendCaseAuditEvent(_ context.Context, arg db.AppendCaseAuditEventParams) (db.AuditEvent, error) {
	f.caseArgs = append(f.caseArgs, arg)
	return db.AuditEvent{
		ID:         arg.ID,
		ScopeType:  string(ScopeCase),
		CaseID:     arg.CaseID,
		EventType:  arg.EventType,
		ActorID:    arg.ActorID,
		ActorRole:  arg.ActorRole,
		AnalysisID: arg.AnalysisID,
		Metadata:   arg.Metadata,
	}, nil
}

func (f *fakeQueries) AppendPolicyAuditEvent(_ context.Context, arg db.AppendPolicyAuditEventParams) (db.AuditEvent, error) {
	f.policyArgs = append(f.policyArgs, arg)
	return db.AuditEvent{
		ID:              arg.ID,
		ScopeType:       string(ScopePolicy),
		PolicyID:        arg.PolicyID,
		PolicyVersionID: arg.PolicyVersionID,
		EventType:       arg.EventType,
		ActorID:         arg.ActorID,
		Metadata:        arg.Metadata,
	}, nil
}

func (f *fakeQueries) GetAnalysis(_ context.Context, id pgtype.UUID) (db.AiAnalysis, error) {
	analysis, ok := f.analyses[id]
	if !ok {
		return db.AiAnalysis{}, errors.New("analysis not found")
	}
	return analysis, nil
}

func (f *fakeQueries) GetPolicyVersion(_ context.Context, id pgtype.UUID) (db.PolicyVersion, error) {
	version, ok := f.policyVersions[id]
	if !ok {
		return db.PolicyVersion{}, errors.New("policy version not found")
	}
	return version, nil
}

func TestAppendCaseEventSetsOnlyCaseScope(t *testing.T) {
	q := &fakeQueries{}
	caseID := testUUID(1)
	actorID := testUUID(2)

	event, err := AppendCaseEvent(context.Background(), q, CaseEvent{
		CaseID:    caseID,
		EventType: "CASE_CREATED",
		ActorID:   actorID,
		ActorRole: pgtype.Text{String: "MAKER", Valid: true},
		Metadata:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("AppendCaseEvent() error = %v", err)
	}
	if len(q.caseArgs) != 1 || len(q.policyArgs) != 0 {
		t.Fatalf("append calls = case %d, policy %d; want case 1, policy 0", len(q.caseArgs), len(q.policyArgs))
	}
	if event.ScopeType != string(ScopeCase) || event.CaseID != caseID {
		t.Errorf("event scope = %q, case_id = %v; want CASE and %v", event.ScopeType, event.CaseID, caseID)
	}
	if event.PolicyID.Valid || event.PolicyVersionID.Valid {
		t.Errorf("policy fields = (%v, %v); want NULL", event.PolicyID, event.PolicyVersionID)
	}
	if !q.caseArgs[0].ID.Valid || q.caseArgs[0].ID.Bytes == ([16]byte{}) {
		t.Errorf("generated ID = %v; want a valid UUID", q.caseArgs[0].ID)
	}
}

func TestAppendPolicyEventSetsOnlyPolicyScope(t *testing.T) {
	q := &fakeQueries{}
	policyID := testUUID(1)
	actorID := testUUID(2)

	event, err := AppendPolicyEvent(context.Background(), q, PolicyEvent{
		PolicyID:  policyID,
		EventType: "POLICY_CREATED",
		ActorID:   actorID,
		Metadata:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("AppendPolicyEvent() error = %v", err)
	}
	if len(q.policyArgs) != 1 || len(q.caseArgs) != 0 {
		t.Fatalf("append calls = policy %d, case %d; want policy 1, case 0", len(q.policyArgs), len(q.caseArgs))
	}
	if event.ScopeType != string(ScopePolicy) || event.PolicyID != policyID {
		t.Errorf("event scope = %q, policy_id = %v; want POLICY and %v", event.ScopeType, event.PolicyID, policyID)
	}
	if event.ActorRole.Valid {
		t.Errorf("actor_role = %v; want NULL", event.ActorRole)
	}
	if event.CaseID.Valid || event.AnalysisID.Valid {
		t.Errorf("case fields = (%v, %v); want NULL", event.CaseID, event.AnalysisID)
	}
	if !q.policyArgs[0].ID.Valid || q.policyArgs[0].ID.Bytes == ([16]byte{}) {
		t.Errorf("generated ID = %v; want a valid UUID", q.policyArgs[0].ID)
	}
}

func TestAppendEventNormalizesEmptyMetadata(t *testing.T) {
	q := &fakeQueries{}
	_, err := AppendCaseEvent(context.Background(), q, CaseEvent{
		CaseID: testUUID(1), EventType: "CASE_UPDATED",
	})
	if err != nil {
		t.Fatalf("AppendCaseEvent() error = %v", err)
	}
	if got := string(q.caseArgs[0].Metadata); got != `{}` {
		t.Errorf("metadata = %q, want {}", got)
	}
}

func TestAppendEventRejectsUnknownType(t *testing.T) {
	q := &fakeQueries{}
	_, err := AppendCaseEvent(context.Background(), q, CaseEvent{
		CaseID: testUUID(1), EventType: "CASE_EXPLODED", Metadata: []byte(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown CASE audit event type") {
		t.Fatalf("AppendCaseEvent() error = %v; want unknown event error", err)
	}
	assertNoAppend(t, q)
}

func TestAppendCaseEventRejectsAnalysisFromDifferentCase(t *testing.T) {
	analysisID := testUUID(3)
	q := &fakeQueries{analyses: map[pgtype.UUID]db.AiAnalysis{
		analysisID: {ID: analysisID, CaseID: testUUID(2)},
	}}
	_, err := AppendCaseEvent(context.Background(), q, CaseEvent{
		CaseID: testUUID(1), AnalysisID: analysisID, EventType: "EXECUTION_STARTED", Metadata: []byte(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "analysis does not belong to case") {
		t.Fatalf("AppendCaseEvent() error = %v; want ownership error", err)
	}
	assertNoAppend(t, q)
}

func TestAppendPolicyEventRejectsVersionFromDifferentPolicy(t *testing.T) {
	versionID := testUUID(3)
	q := &fakeQueries{policyVersions: map[pgtype.UUID]db.PolicyVersion{
		versionID: {ID: versionID, PolicyID: testUUID(2)},
	}}
	_, err := AppendPolicyEvent(context.Background(), q, PolicyEvent{
		PolicyID: testUUID(1), PolicyVersionID: versionID, EventType: "POLICY_ACTIVATED", Metadata: []byte(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "policy version does not belong to policy") {
		t.Fatalf("AppendPolicyEvent() error = %v; want ownership error", err)
	}
	assertNoAppend(t, q)
}

func TestAppendCaseEventValidatesAnalysisFailureType(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		wantErr  bool
	}{
		{name: "verifier failure", metadata: `{"failure_type":"VERIFIER_FAIL"}`},
		{name: "technical retries exhausted", metadata: `{"failure_type":"TECHNICAL_RETRY_EXHAUSTED"}`},
		{name: "unknown failure type", metadata: `{"failure_type":"BOGUS"}`, wantErr: true},
		{name: "missing failure type", metadata: `{}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQueries{}
			_, err := AppendCaseEvent(context.Background(), q, CaseEvent{
				CaseID: testUUID(1), EventType: "AI_ANALYSIS_FAILED", Metadata: []byte(tt.metadata),
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("AppendCaseEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				assertNoAppend(t, q)
			} else if len(q.caseArgs) != 1 {
				t.Fatalf("case append calls = %d, want 1", len(q.caseArgs))
			}
		})
	}
}

func TestAppendCaseEventValidatesReanalysisLimitMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		wantErr  bool
	}{
		{name: "both numeric fields", metadata: `{"latest_analysis_version":3,"max_reanalysis":2}`},
		{name: "missing max reanalysis", metadata: `{"latest_analysis_version":3}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQueries{}
			_, err := AppendCaseEvent(context.Background(), q, CaseEvent{
				CaseID: testUUID(1), EventType: "REANALYSIS_LIMIT_REACHED", Metadata: []byte(tt.metadata),
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("AppendCaseEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				assertNoAppend(t, q)
			} else if len(q.caseArgs) != 1 {
				t.Fatalf("case append calls = %d, want 1", len(q.caseArgs))
			}
		})
	}
}

func assertNoAppend(t *testing.T, q *fakeQueries) {
	t.Helper()
	if len(q.caseArgs) != 0 || len(q.policyArgs) != 0 {
		t.Fatalf("append calls = case %d, policy %d; want zero", len(q.caseArgs), len(q.policyArgs))
	}
}

func testUUID(last byte) pgtype.UUID {
	var value [16]byte
	value[15] = last
	return pgtype.UUID{Bytes: value, Valid: true}
}
