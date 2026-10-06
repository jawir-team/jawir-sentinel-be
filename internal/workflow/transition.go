package workflow

import (
	"errors"
	"fmt"
)

type transitionKey struct {
	from  State
	event Event
}

// transitionTable is the only authority for case state changes.
var transitionTable = map[transitionKey]State{
	{StateDraft, EventSubmit}:                      StateSubmitted,
	{StateSubmitted, EventStartAnalysis}:           StateAIAnalysis,
	{StateAIAnalysis, EventAnalysisSuccess}:        StateChecking,
	{StateAIAnalysis, EventAnalysisFailed}:         StateEscalationRequired,
	{StateAIAnalysis, EventReanalysisLimitReached}: StateEscalationRequired,
	{StateChecking, EventAllCheckersApproved}:      StateSigning,
	{StateChecking, EventCheckerRejected}:          StateAIAnalysis,
	{StateSigning, EventSignerApproved}:            StateExecution,
	{StateSigning, EventSignerRejected}:            StateAIAnalysis,
	{StateExecution, EventExecutionSuccess}:        StateDone,
	{StateExecution, EventExecutionBlocked}:        StateAIAnalysis,
	{StateExecution, EventExecutionFailed}:         StateAIAnalysis,
	{StateDraft, EventClose}:                       StateClosed,
	{StateSubmitted, EventClose}:                   StateClosed,
	{StateAIAnalysis, EventClose}:                  StateClosed,
	{StateChecking, EventClose}:                    StateClosed,
	{StateSigning, EventClose}:                     StateClosed,
	{StateExecution, EventClose}:                   StateClosed,
	{StateEscalationRequired, EventClose}:          StateClosed,
}

// InvalidTransitionError reports a state/event pair for which no transition
// exists.
type InvalidTransitionError struct {
	From  State
	Event Event
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid transition: %s + %s", e.From, e.Event)
}

// Transition returns the state reached by applying event to from.
//
// HTTP handlers must map InvalidTransitionError to HTTP 409 with code
// INVALID_STATE_TRANSITION. They must never update case status directly: all
// state mutation must go through Transition inside a backend service
// transaction.
func Transition(from State, event Event) (State, error) {
	to, ok := transitionTable[transitionKey{from: from, event: event}]
	if !ok {
		return "", &InvalidTransitionError{From: from, Event: event}
	}
	return to, nil
}

// IsInvalidTransition reports whether err is or wraps an
// InvalidTransitionError.
func IsInvalidTransition(err error) bool {
	var target *InvalidTransitionError
	return errors.As(err, &target)
}
