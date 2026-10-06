package workflow

// Event is an event that may cause a case workflow transition.
type Event string

const (
	EventSubmit                 Event = "SUBMIT"
	EventStartAnalysis          Event = "START_ANALYSIS"
	EventAnalysisSuccess        Event = "ANALYSIS_SUCCESS"
	EventAnalysisFailed         Event = "ANALYSIS_FAILED"
	EventReanalysisLimitReached Event = "REANALYSIS_LIMIT_REACHED"
	EventAllCheckersApproved    Event = "ALL_CHECKERS_APPROVED"
	EventCheckerRejected        Event = "CHECKER_REJECTED"
	EventSignerApproved         Event = "SIGNER_APPROVED"
	EventSignerRejected         Event = "SIGNER_REJECTED"
	EventExecutionSuccess       Event = "EXECUTION_SUCCESS"
	EventExecutionBlocked       Event = "EXECUTION_BLOCKED"
	EventExecutionFailed        Event = "EXECUTION_FAILED"
	EventClose                  Event = "CLOSE"
)

// EventAnalysisFailed is only for a terminal AI cycle failure
// (VERIFIER_FAIL or TECHNICAL_RETRY_EXHAUSTED). EventReanalysisLimitReached is
// a distinct event and does not create a new failed analysis version.
