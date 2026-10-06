package workflow

import "sort"

// CanClose reports whether from permits the close event.
func CanClose(from State) bool {
	_, ok := transitionTable[transitionKey{from: from, event: EventClose}]
	return ok
}

// AllowedEvents returns the events accepted from a state in deterministic
// lexical order.
func AllowedEvents(from State) []Event {
	events := make([]Event, 0)
	for key := range transitionTable {
		if key.from == from {
			events = append(events, key.event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i] < events[j]
	})
	return events
}

// IsTerminal reports whether state is terminal.
func IsTerminal(state State) bool {
	return state.Terminal()
}
