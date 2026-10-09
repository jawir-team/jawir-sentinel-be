//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/aiconsumer"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/outboxdispatch"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
)

func TestGoldenPathAcrossCoreOrchestrators(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	maker, checker, signer, executer := user(1, auth.SystemRoleUser), user(2, auth.SystemRoleUser), user(3, auth.SystemRoleUser), user(4, auth.SystemRoleUser)
	seedUsers(store, maker, checker, signer, executer)

	serve(t, handler.CreateCase(store), request(t, http.MethodPost, "/api/v1/cases", map[string]any{
		"case_type_id": store.caseType.ID.String(), "title": "Emergency valve replacement", "urgency": "HIGH",
	}, maker, nil), http.StatusCreated)
	caseID := oneCase(t, store).ID

	assign := func(actor auth.User, role string) {
		serve(t, handler.AssignCaseParticipant(store), request(t, http.MethodPost, "/api/v1/cases/"+caseID.String()+"/participants",
			map[string]any{"user_id": actor.ID.String(), "role": role, "required": true}, maker,
			map[string]string{"id": caseID.String()}), http.StatusCreated)
	}
	assign(checker, "CHECKER")
	assign(signer, "SIGNER")
	assign(executer, "EXECUTER")
	if got, _ := store.ListCaseParticipants(ctx, caseID); len(got) != 4 {
		t.Fatalf("participants=%d want=4", len(got))
	}

	serve(t, handler.SubmitCase(store), request(t, http.MethodPost, "/api/v1/cases/"+caseID.String()+"/submit", nil, maker,
		map[string]string{"id": caseID.String()}), http.StatusOK)
	analysis, outbox := oneAnalysis(t, store), oneOutbox(t, store)
	if analysis.Status != "GENERATING" || outbox.Status != "PENDING" || outbox.EventType != "AI_ANALYSIS_REQUESTED" {
		t.Fatalf("submission rows: analysis=%+v outbox=%+v", analysis, outbox)
	}

	publisher := &capturePublisher{}
	dispatcher := outboxdispatch.NewWithFactory(memoryOutboxFactory{store: store}, publisher)
	if processed, err := dispatcher.DispatchOnce(ctx); err != nil || processed != 1 {
		t.Fatalf("DispatchOnce()=(%d,%v), want (1,nil)", processed, err)
	}
	if len(publisher.messages) != 1 || oneOutbox(t, store).Status != "PUBLISHED" {
		t.Fatalf("publish messages=%d outbox=%+v", len(publisher.messages), oneOutbox(t, store))
	}

	executor := &staticExecutor{execution: successExecution()}
	processor := aiconsumer.New(aiworker.NewWithStore(store, 60), executor, 0)
	if err := processor.Handle(ctx, publisher.messages[0]); err != nil { t.Fatal(err) }
	analysis = oneAnalysis(t, store)
	if analysis.Status != "COMPLETED" || analysis.VerificationStatus.String != "PASS" || oneCase(t, store).Status != "CHECKING" {
		t.Fatalf("worker result: analysis=%+v case=%+v", analysis, oneCase(t, store))
	}

	approve := func(h http.Handler, actor auth.User) {
		serve(t, h, request(t, http.MethodPost, "/decision", map[string]any{
			"analysis_id": analysis.ID.String(), "decision": "APPROVE", "comment": "approved",
		}, actor, map[string]string{"id": caseID.String()}), http.StatusOK)
	}
	reanalysis := &panicReanalysis{}
	approve(handler.RecordCheckerDecision(handler.CheckerDecider{Store: store, Reanalysis: reanalysis}), checker)
	if oneCase(t, store).Status != "SIGNING" { t.Fatalf("after checker status=%s", oneCase(t, store).Status) }
	approve(handler.RecordSignerDecision(handler.SignerDecider{Store: store, Reanalysis: reanalysis}), signer)
	if oneCase(t, store).Status != "EXECUTION" { t.Fatalf("after signer status=%s", oneCase(t, store).Status) }

	serve(t, handler.StartExecution(store), request(t, http.MethodPost, "/execution", map[string]any{"analysis_id": analysis.ID.String()}, executer,
		map[string]string{"id": caseID.String()}), http.StatusCreated)
	execution := oneExecution(t, store)
	serve(t, handler.FinalizeExecutionSuccess(store), request(t, http.MethodPost, "/execution/success", map[string]any{
		"action_taken": "Replaced valve", "result": "Pressure normalized",
	}, executer, map[string]string{"id": caseID.String(), "execution_id": execution.ID.String()}), http.StatusOK)
	if got := oneCase(t, store).Status; got != "DONE" { t.Fatalf("final status=%s want=DONE", got) }

	historyResponse := serve(t, handler.GetCaseHistory(store), request(t, http.MethodGet, "/history", nil, maker,
		map[string]string{"id": caseID.String()}), http.StatusOK)
	var envelope struct { Data []struct { EventType string `json:"event_type"` } `json:"data"` }
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &envelope); err != nil { t.Fatal(err) }
	got := make([]string, 0, len(envelope.Data)); for _, event := range envelope.Data { got = append(got, event.EventType) }
	want := []string{"CASE_CREATED", "PARTICIPANT_ASSIGNED", "PARTICIPANT_ASSIGNED", "PARTICIPANT_ASSIGNED",
		"CASE_SUBMITTED", "AI_ANALYSIS_STARTED", "AI_ANALYSIS_COMPLETED", "CHECKER_APPROVED", "SIGNER_APPROVED",
		"EXECUTION_STARTED", "EXECUTION_SUCCESS", "CASE_DONE"}
	if !reflect.DeepEqual(got, want) { t.Fatalf("history=%v\nwant=%v", got, want) }
	if executor.calls != 1 || store.finalizeRuns != 1 { t.Fatalf("executor calls=%d finalize runs=%d, want 1/1", executor.calls, store.finalizeRuns) }
}

type panicReanalysis struct{}

func (*panicReanalysis) Run(context.Context, reanalysis.Request, reanalysis.PersistAction) (reanalysis.Result, error) {
	panic("approval path must not invoke reanalysis")
}

