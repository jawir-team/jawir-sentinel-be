package reanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

func TestExactThreeReanalysesThenLimit(t *testing.T) {
	tests := []struct {
		name          string
		latest        int32
		wantOutcome   Outcome
		wantVersion   int32
		wantCaseState workflow.State
		wantMetadata  string
	}{
		{name: "v1 allocates v2", latest: 1, wantOutcome: Queued, wantVersion: 2, wantCaseState: workflow.StateAIAnalysis, wantMetadata: `{"version":2}`},
		{name: "v2 allocates v3", latest: 2, wantOutcome: Queued, wantVersion: 3, wantCaseState: workflow.StateAIAnalysis, wantMetadata: `{"version":3}`},
		{name: "v3 allocates v4", latest: 3, wantOutcome: Queued, wantVersion: 4, wantCaseState: workflow.StateAIAnalysis, wantMetadata: `{"version":4}`},
		{name: "v4 reaches limit", latest: 4, wantOutcome: LimitReached, wantCaseState: workflow.StateEscalationRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(workflow.StateChecking, tt.latest)
			service := &Service{runner: runner, maxReanalysis: 3}

			result, err := service.Run(
				context.Background(),
				testRequest(workflow.EventCheckerRejected),
				persistFakeAction(runner),
			)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.Outcome != tt.wantOutcome || result.Case.Status != string(tt.wantCaseState) {
				t.Fatalf("result = %+v, want %s in %s", result, tt.wantOutcome, tt.wantCaseState)
			}
			if runner.state.businessActions != 1 {
				t.Fatalf("business actions = %d, want 1", runner.state.businessActions)
			}

			if tt.wantOutcome == LimitReached {
				if len(runner.state.analyses) != 0 || len(runner.state.outboxes) != 0 {
					t.Fatalf("limit created rows: analyses=%d outboxes=%d", len(runner.state.analyses), len(runner.state.outboxes))
				}
				if result.Analysis != nil || result.Outbox != nil {
					t.Fatalf("limit result contains a job: %+v", result)
				}
				if len(runner.state.audits) != 1 {
					t.Fatalf("audits = %d, want 1", len(runner.state.audits))
				}
				audit := runner.state.audits[0]
				if audit.EventType != auditReanalysisLimit {
					t.Fatalf("event type = %q, want %q", audit.EventType, auditReanalysisLimit)
				}
				if got := string(audit.Metadata); got != `{"latest_analysis_version":4,"max_reanalysis":3}` {
					t.Fatalf("metadata = %s", got)
				}
				if audit.AnalysisID != runner.state.caseRow.CurrentAnalysisID {
					t.Fatalf("audit analysis = %v, want current analysis %v", audit.AnalysisID, runner.state.caseRow.CurrentAnalysisID)
				}
				return
			}

			if len(runner.state.analyses) != 1 {
				t.Fatalf("analyses = %d, want 1", len(runner.state.analyses))
			}
			analysis := runner.state.analyses[0]
			if analysis.Version != tt.wantVersion || analysis.Status != analysisStatusGenerating {
				t.Fatalf("analysis = %+v, want unclaimed GENERATING v%d", analysis, tt.wantVersion)
			}
			if analysis.WorkerAttemptID.Valid || analysis.WorkerStartedAt.Valid {
				t.Fatalf("analysis was prematurely claimed: %+v", analysis)
			}
			if result.Analysis == nil || result.Analysis.ID != analysis.ID {
				t.Fatalf("result analysis = %+v, want %v", result.Analysis, analysis.ID)
			}
			if len(runner.state.audits) != 1 {
				t.Fatalf("audits = %d, want 1", len(runner.state.audits))
			}
			audit := runner.state.audits[0]
			if audit.EventType != auditAnalysisStarted || audit.AnalysisID != analysis.ID || string(audit.Metadata) != tt.wantMetadata {
				t.Fatalf("audit = %+v, want %s with metadata %s", audit, auditAnalysisStarted, tt.wantMetadata)
			}
			if len(runner.state.outboxes) != 1 {
				t.Fatalf("outboxes = %d, want 1", len(runner.state.outboxes))
			}
			outbox := runner.state.outboxes[0]
			if outbox.Status != "PENDING" || outbox.EventType != outboxAnalysisRequested || outbox.AnalysisID != analysis.ID {
				t.Fatalf("outbox = %+v", outbox)
			}
			if result.Outbox == nil || result.Outbox.ID != outbox.ID {
				t.Fatalf("result outbox = %+v, want %v", result.Outbox, outbox.ID)
			}
		})
	}
}

func TestAuditChainReconstructsCause(t *testing.T) {
	runner := newFakeRunner(workflow.StateChecking, 1)
	service := &Service{runner: runner, maxReanalysis: 3}
	request := testRequest(workflow.EventCheckerRejected)

	var latestAnalysisID pgtype.UUID
	for wantVersion := int32(2); wantVersion <= 4; wantVersion++ {
		result, err := service.Run(context.Background(), request, persistFakeAction(runner))
		if err != nil {
			t.Fatalf("queue v%d: %v", wantVersion, err)
		}
		if result.Outcome != Queued || result.Analysis == nil || result.Analysis.Version != wantVersion {
			t.Fatalf("queue v%d result = %+v", wantVersion, result)
		}

		// Simulate the external worker completing this version and returning the
		// case to CHECKING. Technical worker behavior is outside this service.
		latestAnalysisID = result.Analysis.ID
		runner.state.caseRow.CurrentAnalysisID = latestAnalysisID
		runner.state.caseRow.Status = string(workflow.StateChecking)
		runner.latest = wantVersion
	}

	result, err := service.Run(context.Background(), request, persistFakeAction(runner))
	if err != nil {
		t.Fatalf("limit Run() error = %v", err)
	}
	if result.Outcome != LimitReached || result.Case.Status != string(workflow.StateEscalationRequired) {
		t.Fatalf("limit result = %+v", result)
	}
	if len(runner.state.audits) != 4 {
		t.Fatalf("audits = %d, want 4", len(runner.state.audits))
	}

	for i, wantVersion := range []int32{2, 3, 4} {
		audit := runner.state.audits[i]
		if audit.EventType != auditAnalysisStarted {
			t.Fatalf("audit[%d] event = %q, want %q", i, audit.EventType, auditAnalysisStarted)
		}
		var metadata struct {
			Version int32 `json:"version"`
		}
		if err := json.Unmarshal(audit.Metadata, &metadata); err != nil {
			t.Fatalf("decode audit[%d]: %v", i, err)
		}
		if metadata.Version != wantVersion {
			t.Fatalf("audit[%d] version = %d, want %d", i, metadata.Version, wantVersion)
		}
		if audit.ActorID != request.ActorID || !audit.ActorRole.Valid || audit.ActorRole.String != request.ActorRole {
			t.Fatalf("audit[%d] actor = %v/%+v, want %v/%s", i, audit.ActorID, audit.ActorRole, request.ActorID, request.ActorRole)
		}
	}

	limitAudit := runner.state.audits[3]
	if limitAudit.EventType != auditReanalysisLimit {
		t.Fatalf("limit event = %q, want %q", limitAudit.EventType, auditReanalysisLimit)
	}
	if limitAudit.AnalysisID != latestAnalysisID {
		t.Fatalf("limit analysis = %v, want latest %v", limitAudit.AnalysisID, latestAnalysisID)
	}
	if limitAudit.ActorID != request.ActorID || !limitAudit.ActorRole.Valid || limitAudit.ActorRole.String != request.ActorRole {
		t.Fatalf("limit actor = %v/%+v, want %v/%s", limitAudit.ActorID, limitAudit.ActorRole, request.ActorID, request.ActorRole)
	}
	var limitMetadata struct {
		LatestAnalysisVersion int32 `json:"latest_analysis_version"`
		MaxReanalysis         int32 `json:"max_reanalysis"`
	}
	if err := json.Unmarshal(limitAudit.Metadata, &limitMetadata); err != nil {
		t.Fatalf("decode limit audit: %v", err)
	}
	if limitMetadata.LatestAnalysisVersion != 4 || limitMetadata.MaxReanalysis != 3 {
		t.Fatalf("limit metadata = %+v, want latest_analysis_version=4 max_reanalysis=3", limitMetadata)
	}
	if got := limitMetadata.LatestAnalysisVersion - 1; got != limitMetadata.MaxReanalysis {
		t.Fatalf("derived reanalysis_count = %d, max_reanalysis = %d", got, limitMetadata.MaxReanalysis)
	}
}

func TestOnlyGovernedTriggersAccepted(t *testing.T) {
	valid := []struct {
		name    string
		state   workflow.State
		trigger workflow.Event
	}{
		{name: "checker rejected", state: workflow.StateChecking, trigger: workflow.EventCheckerRejected},
		{name: "signer rejected", state: workflow.StateSigning, trigger: workflow.EventSignerRejected},
		{name: "execution blocked", state: workflow.StateExecution, trigger: workflow.EventExecutionBlocked},
		{name: "execution failed", state: workflow.StateExecution, trigger: workflow.EventExecutionFailed},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(tt.state, 1)
			service := &Service{runner: runner, maxReanalysis: 3}
			if _, err := service.Run(context.Background(), testRequest(tt.trigger), persistFakeAction(runner)); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if runner.state.businessActions != 1 || len(runner.state.analyses) != 1 || len(runner.state.audits) != 1 || len(runner.state.outboxes) != 1 {
				t.Fatalf("valid trigger did not commit complete transaction: %+v", runner.state)
			}
		})
	}

	invalid := []workflow.Event{
		workflow.EventSubmit,
		workflow.EventStartAnalysis,
		workflow.EventAnalysisSuccess,
		workflow.EventAnalysisFailed,
		workflow.EventAllCheckersApproved,
		workflow.EventSignerApproved,
		workflow.EventExecutionSuccess,
		workflow.EventClose,
		workflow.EventReanalysisLimitReached,
	}
	for _, trigger := range invalid {
		t.Run(string(trigger), func(t *testing.T) {
			runner := newFakeRunner(workflow.StateChecking, 1)
			service := &Service{runner: runner, maxReanalysis: 3}
			_, err := service.Run(context.Background(), testRequest(trigger), persistFakeAction(runner))
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Run() error = %v, want ErrInvalidRequest", err)
			}
			assertNothingCommitted(t, runner, workflow.StateChecking)
		})
	}
}

func TestTechnicalRetryDoesNotConsumeQuota(t *testing.T) {
	evaluate := func(t *testing.T, latest int32) Result {
		t.Helper()
		runner := newFakeRunner(workflow.StateChecking, latest)
		service := &Service{runner: runner, maxReanalysis: 3}
		result, err := service.Run(
			context.Background(),
			testRequest(workflow.EventCheckerRejected),
			persistFakeAction(runner),
		)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return result
	}

	initial := evaluate(t, 1)
	if initial.Outcome != Queued || initial.Analysis == nil || initial.Analysis.Version != 2 {
		t.Fatalf("initial decision = %+v, want QUEUED v2", initial)
	}

	// analysisrepo.RetryAttempt calls BumpTechnicalRetry, which only increments
	// technical_retry_count and rotates the claim on the same row. It never
	// inserts a version, so the quota input (MaxAnalysisVersion) stays unchanged.
	afterTechnicalRetries := evaluate(t, 1)
	if afterTechnicalRetries.Outcome != initial.Outcome || afterTechnicalRetries.Analysis == nil || afterTechnicalRetries.Analysis.Version != initial.Analysis.Version {
		t.Fatalf("decision after same-version retries = %+v, want same decision as %+v", afterTechnicalRetries, initial)
	}

	afterOneBusinessReanalysis := evaluate(t, 2)
	if afterOneBusinessReanalysis.Outcome != Queued || afterOneBusinessReanalysis.Analysis == nil || afterOneBusinessReanalysis.Analysis.Version != 3 {
		t.Fatalf("decision at latest v2 = %+v, want QUEUED v3", afterOneBusinessReanalysis)
	}
}

func TestNoResumeAfterEscalation(t *testing.T) {
	triggers := []workflow.Event{
		workflow.EventCheckerRejected,
		workflow.EventSignerRejected,
		workflow.EventExecutionBlocked,
		workflow.EventExecutionFailed,
	}
	for _, trigger := range triggers {
		t.Run(string(trigger), func(t *testing.T) {
			runner := newFakeRunner(workflow.StateEscalationRequired, 4)
			service := &Service{runner: runner, maxReanalysis: 3}
			_, err := service.Run(context.Background(), testRequest(trigger), persistFakeAction(runner))
			if !workflow.IsInvalidTransition(err) {
				t.Fatalf("Run() error = %v, want invalid transition", err)
			}
			assertNothingCommitted(t, runner, workflow.StateEscalationRequired)
		})
	}

	got, err := workflow.Transition(workflow.StateEscalationRequired, workflow.EventClose)
	if err != nil || got != workflow.StateClosed {
		t.Fatalf("close transition = %s, %v; want CLOSED, nil", got, err)
	}
}

func TestLimitReachedDistinctFromAnalysisFailed(t *testing.T) {
	runner := newFakeRunner(workflow.StateChecking, 4)
	service := &Service{runner: runner, maxReanalysis: 3}

	result, err := service.Run(
		context.Background(),
		testRequest(workflow.EventCheckerRejected),
		persistFakeAction(runner),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != LimitReached {
		t.Fatalf("outcome = %q, want %q", result.Outcome, LimitReached)
	}
	if len(runner.state.analyses) != 0 {
		t.Fatalf("analyses = %+v, want none", runner.state.analyses)
	}
	if len(runner.state.audits) != 1 || runner.state.audits[0].EventType != string(workflow.EventReanalysisLimitReached) {
		t.Fatalf("audits = %+v, want only REANALYSIS_LIMIT_REACHED", runner.state.audits)
	}
	if runner.state.audits[0].EventType == string(workflow.EventAnalysisFailed) {
		t.Fatalf("limit audit was recorded as %q", workflow.EventAnalysisFailed)
	}
	if workflow.EventReanalysisLimitReached == workflow.EventAnalysisFailed {
		t.Fatal("reanalysis limit and analysis failure events must be distinct")
	}
}

func assertNothingCommitted(t *testing.T, runner *fakeRunner, wantState workflow.State) {
	t.Helper()
	if runner.state.businessActions != 0 || len(runner.state.analyses) != 0 || len(runner.state.audits) != 0 || len(runner.state.outboxes) != 0 {
		t.Fatalf("transaction committed unexpected work: %+v", runner.state)
	}
	if runner.state.caseRow.Status != string(wantState) || len(runner.state.statusUpdates) != 0 {
		t.Fatalf("case changed after rollback/rejection: %+v", runner.state)
	}
}
