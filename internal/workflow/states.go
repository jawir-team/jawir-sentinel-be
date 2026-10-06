package workflow

// State is a case workflow state.
type State string

const (
	StateDraft              State = "DRAFT"
	StateSubmitted          State = "SUBMITTED"
	StateAIAnalysis         State = "AI_ANALYSIS"
	StateChecking           State = "CHECKING"
	StateSigning            State = "SIGNING"
	StateExecution          State = "EXECUTION"
	StateDone               State = "DONE"
	StateClosed             State = "CLOSED"
	StateEscalationRequired State = "ESCALATION_REQUIRED"
)

// Valid reports whether s is a known workflow state.
func (s State) Valid() bool {
	switch s {
	case StateDraft,
		StateSubmitted,
		StateAIAnalysis,
		StateChecking,
		StateSigning,
		StateExecution,
		StateDone,
		StateClosed,
		StateEscalationRequired:
		return true
	default:
		return false
	}
}

// Terminal reports whether s has no outgoing transitions.
func (s State) Terminal() bool {
	return s == StateDone || s == StateClosed
}
