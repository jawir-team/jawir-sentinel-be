package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

var allEvents = []Event{
	EventSubmit,
	EventStartAnalysis,
	EventAnalysisSuccess,
	EventAnalysisFailed,
	EventReanalysisLimitReached,
	EventAllCheckersApproved,
	EventCheckerRejected,
	EventSignerApproved,
	EventSignerRejected,
	EventExecutionSuccess,
	EventExecutionBlocked,
	EventExecutionFailed,
	EventClose,
}

var allStates = []State{
	StateDraft,
	StateSubmitted,
	StateAIAnalysis,
	StateChecking,
	StateSigning,
	StateExecution,
	StateDone,
	StateClosed,
	StateEscalationRequired,
}

func TestLockedTransitionMatrix(t *testing.T) {
	legal := map[transitionKey]State{
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
	if !reflect.DeepEqual(transitionTable, legal) {
		t.Fatalf("transitionTable = %#v, want locked matrix %#v", transitionTable, legal)
	}

	for _, from := range allStates {
		for _, event := range allEvents {
			from, event := from, event
			t.Run(string(from)+"/"+string(event), func(t *testing.T) {
				want, allowed := legal[transitionKey{from: from, event: event}]
				if !allowed {
					assertInvalidTransition(t, from, event)
					return
				}

				got, err := Transition(from, event)
				if err != nil {
					t.Fatalf("Transition(%q, %q) error = %v", from, event, err)
				}
				if got != want {
					t.Fatalf("Transition(%q, %q) = %q, want %q", from, event, got, want)
				}
			})
		}
	}
}

func TestLockedTransitions(t *testing.T) {
	tests := []struct {
		name  string
		from  State
		event Event
		want  State
	}{
		{"submit", StateDraft, EventSubmit, StateSubmitted},
		{"start analysis", StateSubmitted, EventStartAnalysis, StateAIAnalysis},
		{"analysis success", StateAIAnalysis, EventAnalysisSuccess, StateChecking},
		{"analysis failed", StateAIAnalysis, EventAnalysisFailed, StateEscalationRequired},
		{"reanalysis limit reached", StateAIAnalysis, EventReanalysisLimitReached, StateEscalationRequired},
		{"all checkers approved", StateChecking, EventAllCheckersApproved, StateSigning},
		{"checker rejected", StateChecking, EventCheckerRejected, StateAIAnalysis},
		{"signer approved", StateSigning, EventSignerApproved, StateExecution},
		{"signer rejected", StateSigning, EventSignerRejected, StateAIAnalysis},
		{"execution success", StateExecution, EventExecutionSuccess, StateDone},
		{"execution blocked", StateExecution, EventExecutionBlocked, StateAIAnalysis},
		{"execution failed", StateExecution, EventExecutionFailed, StateAIAnalysis},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Transition(tt.from, tt.event)
			if err != nil {
				t.Fatalf("Transition() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Transition() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnalysisFailureEventsAreDistinct(t *testing.T) {
	if EventAnalysisFailed == EventReanalysisLimitReached {
		t.Fatal("analysis failure events must be distinct")
	}

	for _, event := range []Event{EventAnalysisFailed, EventReanalysisLimitReached} {
		got, err := Transition(StateAIAnalysis, event)
		if err != nil {
			t.Fatalf("Transition(%q, %q) error = %v", StateAIAnalysis, event, err)
		}
		if got != StateEscalationRequired {
			t.Fatalf("Transition(%q, %q) = %q, want %q", StateAIAnalysis, event, got, StateEscalationRequired)
		}
	}
}

func TestCloseFromEveryNonTerminalState(t *testing.T) {
	states := []State{
		StateDraft,
		StateSubmitted,
		StateAIAnalysis,
		StateChecking,
		StateSigning,
		StateExecution,
		StateEscalationRequired,
	}

	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			got, err := Transition(state, EventClose)
			if err != nil {
				t.Fatalf("Transition() error = %v", err)
			}
			if got != StateClosed {
				t.Fatalf("Transition() = %q, want %q", got, StateClosed)
			}
		})
	}
}

func TestEscalationRequiredRejectsEveryNonCloseEvent(t *testing.T) {
	for _, event := range allEvents {
		if event == EventClose {
			continue
		}
		assertInvalidTransition(t, StateEscalationRequired, event)
	}
}

func TestTerminalStatesRejectEveryEvent(t *testing.T) {
	for _, state := range []State{StateDone, StateClosed} {
		for _, event := range allEvents {
			assertInvalidTransition(t, state, event)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	tests := []struct {
		name  string
		from  State
		event Event
	}{
		{"draft checker approval", StateDraft, EventAllCheckersApproved},
		{"checking submit", StateChecking, EventSubmit},
		{"submitted submit", StateSubmitted, EventSubmit},
		{"unknown event", StateDraft, Event("BOGUS")},
		{"unknown state", State("BOGUS"), EventSubmit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertInvalidTransition(t, tt.from, tt.event)
		})
	}
}

func TestInvalidTransitionError(t *testing.T) {
	err := &InvalidTransitionError{From: StateDraft, Event: EventSignerApproved}
	want := "invalid transition: DRAFT + SIGNER_APPROVED"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !IsInvalidTransition(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("IsInvalidTransition() = false for wrapped InvalidTransitionError")
	}
	if IsInvalidTransition(errors.New("other error")) {
		t.Fatal("IsInvalidTransition() = true for unrelated error")
	}
}

func TestStateGuards(t *testing.T) {
	validStates := []State{
		StateDraft,
		StateSubmitted,
		StateAIAnalysis,
		StateChecking,
		StateSigning,
		StateExecution,
		StateDone,
		StateClosed,
		StateEscalationRequired,
	}
	for _, state := range validStates {
		if !state.Valid() {
			t.Errorf("State(%q).Valid() = false", state)
		}
	}
	if State("BOGUS").Valid() {
		t.Error("unknown state Valid() = true")
	}

	for _, state := range []State{StateDone, StateClosed} {
		if !state.Terminal() || !IsTerminal(state) {
			t.Errorf("state %q is not terminal", state)
		}
		if CanClose(state) {
			t.Errorf("CanClose(%q) = true", state)
		}
	}
	for _, state := range []State{StateDraft, StateSubmitted, StateAIAnalysis, StateChecking, StateSigning, StateExecution, StateEscalationRequired} {
		if state.Terminal() || IsTerminal(state) {
			t.Errorf("state %q is terminal", state)
		}
		if !CanClose(state) {
			t.Errorf("CanClose(%q) = false", state)
		}
	}
	if CanClose(State("BOGUS")) {
		t.Error("CanClose(BOGUS) = true")
	}
}

func TestAllowedEvents(t *testing.T) {
	tests := []struct {
		state State
		want  []Event
	}{
		{StateDraft, []Event{EventClose, EventSubmit}},
		{StateAIAnalysis, []Event{EventAnalysisFailed, EventAnalysisSuccess, EventClose, EventReanalysisLimitReached}},
		{StateEscalationRequired, []Event{EventClose}},
		{StateDone, []Event{}},
		{StateClosed, []Event{}},
		{State("BOGUS"), []Event{}},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			got := AllowedEvents(tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AllowedEvents() = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertInvalidTransition(t *testing.T, from State, event Event) {
	t.Helper()

	got, err := Transition(from, event)
	if err == nil {
		t.Fatalf("Transition(%q, %q) = %q, want error", from, event, got)
	}
	if got != "" {
		t.Errorf("Transition(%q, %q) state = %q, want empty", from, event, got)
	}
	if !IsInvalidTransition(err) {
		t.Fatalf("Transition(%q, %q) error type = %T, want *InvalidTransitionError", from, event, err)
	}

	var transitionErr *InvalidTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("errors.As(%T) = false", err)
	}
	if transitionErr.From != from || transitionErr.Event != event {
		t.Errorf("InvalidTransitionError = %+v, want From %q Event %q", transitionErr, from, event)
	}
}
