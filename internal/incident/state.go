// Package incident owns the monitor state machine (docs/10_INCIDENTS.md).
// Transition is pure: it decides, the result processor persists.
package incident

// State is a stored monitor state; the values match monitors.current_state.
// FLAPPING is an overlay and TLS expiry a warning: neither is a state.
type State string

const (
	Up      State = "up"
	Pending State = "pending"
	Down    State = "down"
	Paused  State = "paused"
)

// Counters are the consecutive results that have not yet changed the state:
// failures while up or pending, successes while down.
type Counters struct {
	Failures  int
	Successes int
}

// Thresholds are the monitor's failure and success thresholds. Values below
// 1 count as 1.
type Thresholds struct {
	Failure int
	Success int
}

// Outcome is the decision for one check result.
type Outcome struct {
	State    State
	Counters Counters
	// Retry asks for one confirmation check after the monitor's retry delay
	// instead of waiting for the next interval. It is only set while
	// pending, so a monitor gets at most Failure-1 retries in a row.
	Retry bool
}

// Transition applies one check result.
//
//	up, pending + success                 -> up
//	up, pending + failure below threshold -> pending, retry
//	up, pending + failure at threshold    -> down
//	down + failure                        -> down
//	down + success below threshold        -> down
//	down + success at threshold           -> up
//	paused (or an unknown state)          -> unchanged
//
// A new or resumed monitor is pending with zero counters and follows the
// same rules as up.
func Transition(s State, c Counters, success bool, t Thresholds) Outcome {
	switch s {
	case Up, Pending:
		if success {
			return Outcome{State: Up}
		}
		failures := c.Failures + 1
		if failures >= max(t.Failure, 1) {
			return Outcome{State: Down}
		}
		return Outcome{State: Pending, Counters: Counters{Failures: failures}, Retry: true}
	case Down:
		if !success {
			return Outcome{State: Down}
		}
		successes := c.Successes + 1
		if successes >= max(t.Success, 1) {
			return Outcome{State: Up}
		}
		return Outcome{State: Down, Counters: Counters{Successes: successes}}
	default:
		// A result for a paused monitor is a check that was already running
		// when it was paused: it changes nothing.
		return Outcome{State: s, Counters: c}
	}
}
