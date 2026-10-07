package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
		AnalysisID: queries.caseResult.CurrentAnalysisID,
		ActorID:    checkerID,
		ActorRole:  "CHECKER",
		Decision:   "APPROVE",
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

	response, store, orchestrator := serveSignerDecision(actor, queries, reanalysis.Queued,
		`{"analysis_id":"00000000-0000-0000-0000-000000000005","decision":"APPROVE","comment":"Authorized"}`)

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
