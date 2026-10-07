package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

func signerDecisionFixture(actor auth.User) *fakeCheckerDecisionQueries {
	queries := checkerDecisionFixture(actor)
	queries.caseResult.Status = string(workflow.StateSigning)
	queries.participants[0].Role = "SIGNER"
	checkerID := handlerTestUUID(8)
	queries.participants = append(queries.participants, db.CaseParticipant{
		ID: handlerTestUUID(11), CaseID: queries.caseResult.ID, UserID: checkerID,
		Role: "CHECKER", Required: true, Status: "ACTIVE",
	})
	queries.decisions = []db.Decision{{
		ID:         handlerTestUUID(12),
		AnalysisID: queries.caseResult.CurrentAnalysisID,
		ActorID:    checkerID,
		ActorRole:  "CHECKER",
		Decision:   "APPROVE",
		CreatedAt:  caseTestTime.Add(-time.Minute),
	}}
	return queries
}

func serveSignerDecision(actor auth.User, queries *fakeCheckerDecisionQueries, outcome reanalysis.Outcome, body string) (*httptest.ResponseRecorder, *fakeCheckerDecisionStore, *fakeCheckerReanalysis) {
	store := &fakeCheckerDecisionStore{queries: queries}
	orchestrator := &fakeCheckerReanalysis{queries: queries, outcome: outcome}
	request := caseRequestWithID(
		http.MethodPost,
		"/api/v1/cases/00000000-0000-0000-0000-000000000004/signer-decisions",
		handlerTestUUID(4).String(), body, &actor,
	)
	response := httptest.NewRecorder()
	handler.RecordSignerDecision(handler.SignerDecider{Store: store, Reanalysis: orchestrator}).ServeHTTP(response, request)
	return response, store, orchestrator
}

func TestRecordSignerDecisionApproveAdvancesToExecution(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	policyVersionID := handlerTestUUID(13)
	queries.policyRefs = []db.AnalysisPolicyRef{{
		ID: handlerTestUUID(14), AnalysisID: queries.analysisResult.ID,
		PolicyVersionID: policyVersionID,
		Section:         pgtype.Text{String: "4.2", Valid: true},
		Excerpt:         pgtype.Text{String: "Immediate repairs require authorization.", Valid: true},
	}}
	queries.policyVersions[policyVersionID] = db.PolicyVersion{
		ID: policyVersionID, PolicyID: handlerTestUUID(15), Version: "2026.1", Status: "SUPERSEDED",
	}
	queries.evidenceRefs = []db.AnalysisEvidenceRef{{
		ID: handlerTestUUID(16), AnalysisID: queries.analysisResult.ID,
		EvidenceID: handlerTestUUID(6), UsageType: "SUPPORTING_FACT",
	}}

	response, store, orchestrator := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE","comment":"Authorized","evidence_ids":["00000000-0000-0000-0000-000000000006"]}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "EXECUTION")
	if store.calls != 1 || orchestrator.calls != 0 {
		t.Errorf("calls = store %d reanalysis %d, want 1 and 0", store.calls, orchestrator.calls)
	}
	if queries.createArg.ActorRole != "SIGNER" || queries.updateCalls != 1 || queries.updateArg.Status != "EXECUTION" {
		t.Errorf("decision/update = %+v / %+v, want SIGNER and EXECUTION", queries.createArg, queries.updateArg)
	}
	if queries.auditArg.EventType != "SIGNER_APPROVED" || !queries.auditArg.ActorRole.Valid || queries.auditArg.ActorRole.String != "SIGNER" {
		t.Errorf("audit = %+v, want SIGNER_APPROVED by SIGNER", queries.auditArg)
	}
	if queries.evidenceCalls != 0 {
		t.Errorf("feedback evidence calls = %d, want 0", queries.evidenceCalls)
	}

	var metadata struct {
		AnalysisID  string   `json:"analysis_id"`
		Decision    string   `json:"decision"`
		Reason      string   `json:"reason"`
		Comment     string   `json:"comment"`
		EvidenceIDs []string `json:"evidence_ids"`
		Snapshot    struct {
			Case struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				CaseTypeID   string `json:"case_type_id"`
				CaseTypeCode string `json:"case_type_code"`
				CaseTypeName string `json:"case_type_name"`
				Status       string `json:"status"`
			} `json:"case"`
			Analysis struct {
				ID      string `json:"id"`
				Version int32  `json:"version"`
			} `json:"analysis"`
			PolicyVersions []struct {
				AnalysisPolicyRefID string  `json:"analysis_policy_ref_id"`
				PolicyID            string  `json:"policy_id"`
				PolicyVersionID     string  `json:"policy_version_id"`
				Version             string  `json:"version"`
				Status              string  `json:"status"`
				Section             *string `json:"section"`
				Excerpt             *string `json:"excerpt"`
			} `json:"policy_versions"`
			EvidenceReferences []struct {
				AnalysisEvidenceRefID string `json:"analysis_evidence_ref_id"`
				EvidenceID            string `json:"evidence_id"`
				UsageType             string `json:"usage_type"`
			} `json:"evidence_references"`
			CitedEvidenceIDs []string `json:"cited_evidence_ids"`
			CheckerApprovals struct {
				RequiredCount int `json:"required_count"`
				ApprovedCount int `json:"approved_count"`
				Approvals     []struct {
					ActorID    string    `json:"actor_id"`
					Role       string    `json:"role"`
					DecisionID string    `json:"decision_id"`
					Decision   string    `json:"decision"`
					DecidedAt  time.Time `json:"decided_at"`
				} `json:"approvals"`
			} `json:"checker_approvals"`
			Signer struct {
				ActorID string `json:"actor_id"`
				Role    string `json:"role"`
			} `json:"signer"`
			Recommendation map[string]any `json:"recommendation"`
			ApprovedAt     time.Time      `json:"approved_at"`
		} `json:"decision_snapshot"`
	}
	if err := json.Unmarshal(queries.auditArg.Metadata, &metadata); err != nil {
		t.Fatalf("decode signer approval audit metadata: %v", err)
	}
	if metadata.AnalysisID != queries.analysisResult.ID.String() || metadata.Decision != "APPROVE" || metadata.Reason != "" || metadata.Comment != "Authorized" {
		t.Errorf("preserved audit metadata = %+v", metadata)
	}
	if len(metadata.EvidenceIDs) != 1 || metadata.EvidenceIDs[0] != handlerTestUUID(6).String() {
		t.Errorf("audit evidence_ids = %v", metadata.EvidenceIDs)
	}
	if metadata.Snapshot.Case.ID != queries.caseResult.ID.String() || metadata.Snapshot.Case.Title != "Leaking pipe" || metadata.Snapshot.Case.CaseTypeID != handlerTestUUID(3).String() || metadata.Snapshot.Case.CaseTypeCode != "OPERATIONAL_INCIDENT" || metadata.Snapshot.Case.CaseTypeName != "Operational Incident" || metadata.Snapshot.Case.Status != "SIGNING" {
		t.Errorf("case snapshot = %+v", metadata.Snapshot.Case)
	}
	if metadata.Snapshot.Analysis.ID != queries.analysisResult.ID.String() || metadata.Snapshot.Analysis.Version != 1 {
		t.Errorf("analysis snapshot = %+v", metadata.Snapshot.Analysis)
	}
	if len(metadata.Snapshot.PolicyVersions) != 1 || metadata.Snapshot.PolicyVersions[0].PolicyVersionID != policyVersionID.String() || metadata.Snapshot.PolicyVersions[0].Version != "2026.1" || metadata.Snapshot.PolicyVersions[0].Status != "SUPERSEDED" || metadata.Snapshot.PolicyVersions[0].Section == nil || *metadata.Snapshot.PolicyVersions[0].Section != "4.2" {
		t.Errorf("policy snapshot = %+v", metadata.Snapshot.PolicyVersions)
	}
	if len(metadata.Snapshot.EvidenceReferences) != 1 || metadata.Snapshot.EvidenceReferences[0].EvidenceID != handlerTestUUID(6).String() || metadata.Snapshot.EvidenceReferences[0].UsageType != "SUPPORTING_FACT" {
		t.Errorf("analysis evidence snapshot = %+v", metadata.Snapshot.EvidenceReferences)
	}
	if len(metadata.Snapshot.CitedEvidenceIDs) != 1 || metadata.Snapshot.CitedEvidenceIDs[0] != handlerTestUUID(6).String() {
		t.Errorf("cited evidence snapshot = %v", metadata.Snapshot.CitedEvidenceIDs)
	}
	if metadata.Snapshot.CheckerApprovals.RequiredCount != 1 || metadata.Snapshot.CheckerApprovals.ApprovedCount != 1 || len(metadata.Snapshot.CheckerApprovals.Approvals) != 1 || metadata.Snapshot.CheckerApprovals.Approvals[0].DecisionID != handlerTestUUID(12).String() {
		t.Errorf("checker approval snapshot = %+v", metadata.Snapshot.CheckerApprovals)
	}
	if metadata.Snapshot.Signer.ActorID != actor.ID.String() || metadata.Snapshot.Signer.Role != "SIGNER" {
		t.Errorf("signer snapshot = %+v", metadata.Snapshot.Signer)
	}
	if metadata.Snapshot.Recommendation["summary"] != "Repair immediately" || !metadata.Snapshot.ApprovedAt.Equal(caseTestTime) {
		t.Errorf("recommendation/approved_at = %v / %v", metadata.Snapshot.Recommendation, metadata.Snapshot.ApprovedAt)
	}
}

func TestRecordSignerDecisionSnapshotUsesReferencedPolicyVersion(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	referencedVersionID := handlerTestUUID(13)
	currentActiveVersionID := handlerTestUUID(14)
	queries.policyRefs = []db.AnalysisPolicyRef{{
		ID: handlerTestUUID(15), AnalysisID: queries.analysisResult.ID, PolicyVersionID: referencedVersionID,
	}}
	queries.policyVersions[referencedVersionID] = db.PolicyVersion{
		ID: referencedVersionID, PolicyID: handlerTestUUID(16), Version: "1", Status: "SUPERSEDED",
	}
	queries.policyVersions[currentActiveVersionID] = db.PolicyVersion{
		ID: currentActiveVersionID, PolicyID: handlerTestUUID(16), Version: "2", Status: "ACTIVE",
	}

	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "EXECUTION")
	var metadata struct {
		Snapshot struct {
			PolicyVersions []struct {
				PolicyVersionID string `json:"policy_version_id"`
				Version         string `json:"version"`
				Status          string `json:"status"`
			} `json:"policy_versions"`
		} `json:"decision_snapshot"`
	}
	if err := json.Unmarshal(queries.auditArg.Metadata, &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if len(metadata.Snapshot.PolicyVersions) != 1 || metadata.Snapshot.PolicyVersions[0].PolicyVersionID != referencedVersionID.String() || metadata.Snapshot.PolicyVersions[0].Version != "1" || metadata.Snapshot.PolicyVersions[0].Status != "SUPERSEDED" {
		t.Errorf("policy versions = %+v, want only frozen referenced version", metadata.Snapshot.PolicyVersions)
	}
}

func TestRecordSignerDecisionSnapshotCheckerApprovalsUseExactAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.decisions = append(queries.decisions,
		db.Decision{
			ID: handlerTestUUID(13), AnalysisID: handlerTestUUID(7), ActorID: handlerTestUUID(9),
			ActorRole: "CHECKER", Decision: "APPROVE", CreatedAt: caseTestTime.Add(-time.Hour),
		},
		db.Decision{
			ID: handlerTestUUID(14), AnalysisID: queries.analysisResult.ID, ActorID: handlerTestUUID(10),
			ActorRole: "SIGNER", Decision: "APPROVE", CreatedAt: caseTestTime.Add(-time.Minute),
		},
	)

	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCheckerDecisionSuccess(t, response, "APPROVE", "EXECUTION")
	var metadata struct {
		Snapshot struct {
			CheckerApprovals struct {
				Approvals []struct {
					DecisionID string `json:"decision_id"`
				} `json:"approvals"`
			} `json:"checker_approvals"`
		} `json:"decision_snapshot"`
	}
	if err := json.Unmarshal(queries.auditArg.Metadata, &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	approvals := metadata.Snapshot.CheckerApprovals.Approvals
	if len(approvals) != 1 || approvals[0].DecisionID != handlerTestUUID(12).String() {
		t.Errorf("checker approvals = %+v, want only exact-analysis checker decision", approvals)
	}
}

func TestRecordSignerDecisionRejectQueuesReanalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.evidences = append(queries.evidences, db.CaseEvidence{ID: handlerTestUUID(7), CaseID: queries.caseResult.ID})

	response, store, orchestrator := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":" Missing authority ","comment":" Escalate first ","evidence_ids":["00000000-0000-0000-0000-000000000007"]}`)

	assertCheckerDecisionSuccess(t, response, "REJECT", "AI_ANALYSIS")
	if store.calls != 0 || orchestrator.calls != 1 {
		t.Errorf("calls = store %d reanalysis %d, want 0 and 1", store.calls, orchestrator.calls)
	}
	if orchestrator.request.Trigger != workflow.EventSignerRejected || orchestrator.request.ActorRole != "SIGNER" {
		t.Errorf("reanalysis request = %+v", orchestrator.request)
	}
	if queries.createArg.ActorRole != "SIGNER" || !queries.createArg.Reason.Valid || queries.createArg.Reason.String != "Missing authority" {
		t.Errorf("decision = %+v, want SIGNER rejection with trimmed reason", queries.createArg)
	}
	if queries.evidenceCalls != 1 || queries.evidenceArg.SourceType != "SIGNER" || queries.evidenceArg.Title.String != "Signer rejection feedback" {
		t.Errorf("feedback evidence = calls %d arg %+v", queries.evidenceCalls, queries.evidenceArg)
	}
	if queries.auditArg.EventType != "SIGNER_REJECTED" {
		t.Errorf("audit event = %q, want SIGNER_REJECTED", queries.auditArg.EventType)
	}
	if got := fmt.Sprint(queries.order); got != "[decision evidence audit]" {
		t.Errorf("mutation order = %s, want [decision evidence audit]", got)
	}
	var metadata struct {
		FeedbackEvidenceID string `json:"feedback_evidence_id"`
	}
	if err := json.Unmarshal(queries.auditArg.Metadata, &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata.FeedbackEvidenceID != queries.evidenceArg.ID.String() {
		t.Errorf("feedback evidence id = %q, want %q", metadata.FeedbackEvidenceID, queries.evidenceArg.ID.String())
	}
	var rawMetadata map[string]json.RawMessage
	if err := json.Unmarshal(queries.auditArg.Metadata, &rawMetadata); err != nil {
		t.Fatalf("decode rejection audit metadata keys: %v", err)
	}
	if _, exists := rawMetadata["decision_snapshot"]; exists {
		t.Errorf("signer rejection audit metadata = %s, must not contain decision_snapshot", queries.auditArg.Metadata)
	}
}

func TestRecordSignerDecisionRejectLimitReachedIsSuccess(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)

	response, _, orchestrator := serveSignerDecision(actor, queries, reanalysis.LimitReached,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":"Cannot sign"}`)

	assertCheckerDecisionSuccess(t, response, "REJECT", "ESCALATION_REQUIRED")
	if queries.createCalls != 1 || queries.evidenceCalls != 1 || queries.auditCalls != 1 {
		t.Errorf("persisted mutations = decision %d evidence %d audit %d, want one each", queries.createCalls, queries.evidenceCalls, queries.auditCalls)
	}
	if orchestrator.result.Analysis != nil {
		t.Errorf("limit-path analysis = %+v, want nil", orchestrator.result.Analysis)
	}
}

func TestRecordSignerDecisionStaleAnalysis(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000007","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeStaleAnalysis)
}

func TestRecordSignerDecisionCheckerCannotSign(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.participants[0].Role = "CHECKER"
	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusForbidden, httpapi.CodeForbidden)
}

func TestRecordSignerDecisionRequiresAllCheckerApprovals(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.decisions = nil
	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"REJECT","reason":"Not ready"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
	if queries.createCalls != 0 || queries.evidenceCalls != 0 || queries.auditCalls != 0 {
		t.Errorf("mutation calls = decision %d evidence %d audit %d, want zero", queries.createCalls, queries.evidenceCalls, queries.auditCalls)
	}
}

func TestRecordSignerDecisionDuplicate(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.decisions = append(queries.decisions, db.Decision{
		AnalysisID: queries.caseResult.CurrentAnalysisID,
		ActorID:    actor.ID, ActorRole: "SIGNER", Decision: "APPROVE",
	})
	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeConflict)
}

func TestRecordSignerDecisionRequiresSigningState(t *testing.T) {
	actor := caseTestUser(auth.SystemRoleUser)
	queries := signerDecisionFixture(actor)
	queries.caseResult.Status = string(workflow.StateChecking)
	response, _, _ := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE"}`)

	assertCaseTypeAPIError(t, response, http.StatusConflict, httpapi.CodeInvalidStateTransition)
}
