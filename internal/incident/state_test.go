package incident

import "testing"

// TestTransitionExhaustive checks every state, result, threshold pair and
// counter value against the rules of docs/10, written out independently of
// the implementation's control flow.
func TestTransitionExhaustive(t *testing.T) {
	for _, s := range []State{Up, Pending, Down, Paused, State("degraded")} {
		for _, success := range []bool{true, false} {
			for f := 1; f <= 3; f++ {
				for sT := 1; sT <= 3; sT++ {
					for failures := 0; failures <= 3; failures++ {
						for successes := 0; successes <= 3; successes++ {
							c := Counters{Failures: failures, Successes: successes}
							got := Transition(s, c, success, Thresholds{Failure: f, Success: sT})
							var want Outcome
							switch {
							case s != Up && s != Pending && s != Down:
								want = Outcome{State: s, Counters: c}
							case s != Down && success:
								want = Outcome{State: Up}
							case s != Down && failures+1 >= f:
								want = Outcome{State: Down}
							case s != Down:
								want = Outcome{State: Pending, Counters: Counters{Failures: failures + 1}, Retry: true}
							case !success:
								want = Outcome{State: Down}
							case successes+1 >= sT:
								want = Outcome{State: Up}
							default:
								want = Outcome{State: Down, Counters: Counters{Successes: successes + 1}}
							}
							if got != want {
								t.Errorf("Transition(%s, %+v, success=%v, F=%d S=%d) = %+v, want %+v", s, c, success, f, sT, got, want)
							}
						}
					}
				}
			}
		}
	}
}

// run feeds a sequence of results ('S' success, 'F' failure) and returns the
// states reached and the number of retries asked for.
func run(start State, results string, t Thresholds) (states string, retries int) {
	s, c := start, Counters{}
	for _, r := range results {
		o := Transition(s, c, r == 'S', t)
		s, c = o.State, o.Counters
		if o.Retry {
			retries++
		}
		states += " " + string(s)
	}
	return states[1:], retries
}

func TestTransitionFlows(t *testing.T) {
	def := Thresholds{Failure: 2, Success: 1}
	tests := []struct {
		name    string
		start   State
		results string
		th      Thresholds
		states  string
		retries int
	}{
		{"healthy, failure, retry fails: down", Up, "FF", def, "pending down", 1},
		{"healthy, failure, retry succeeds: no outage", Up, "FS", def, "pending up", 1},
		{"down, success: recovered", Down, "S", def, "up", 0},
		{"down stays down without retries", Down, "FFF", def, "down down down", 0},
		{"new monitor, first check passes", Pending, "S", def, "up", 0},
		{"new monitor, first check fails twice", Pending, "FF", def, "pending down", 1},
		{"threshold 1 goes straight down", Up, "F", Thresholds{Failure: 1, Success: 1}, "down", 0},
		{"threshold 3 needs three failures and two retries", Up, "FFF", Thresholds{Failure: 3, Success: 1}, "pending pending down", 2},
		{"a success resets the failure count", Up, "FFSFF", Thresholds{Failure: 3, Success: 1}, "pending pending up pending pending", 4},
		{"recovery threshold 3", Down, "SSS", Thresholds{Failure: 2, Success: 3}, "down down up", 0},
		{"a failure resets the recovery count", Down, "SSFSSS", Thresholds{Failure: 2, Success: 3}, "down down down down down up", 0},
		{"full cycle", Up, "SFFFSFS", def, "up pending down down up pending up", 2},
		{"paused ignores results", Paused, "FFS", def, "paused paused paused", 0},
		{"thresholds below 1 count as 1", Up, "FS", Thresholds{}, "down up", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			states, retries := run(tc.start, tc.results, tc.th)
			if states != tc.states || retries != tc.retries {
				t.Errorf("got %q with %d retries, want %q with %d", states, retries, tc.states, tc.retries)
			}
		})
	}
}

// Retries never chain without bound: a monitor that keeps failing gets
// exactly threshold-1 of them, then none while it stays down.
func TestTransitionRetriesAreBounded(t *testing.T) {
	for f := 1; f <= 100; f++ {
		s, c, retries := Up, Counters{}, 0
		for range 500 {
			o := Transition(s, c, false, Thresholds{Failure: f, Success: 1})
			s, c = o.State, o.Counters
			if o.Retry {
				retries++
				if s != Pending {
					t.Fatalf("F=%d: retry requested in state %s", f, s)
				}
			}
		}
		if retries != f-1 || s != Down {
			t.Fatalf("F=%d: %d retries, final state %s", f, retries, s)
		}
	}
}

func TestStateValuesMatchSchema(t *testing.T) {
	// monitors.current_state CHECK (… IN ('up','pending','down','paused')).
	if Up != "up" || Pending != "pending" || Down != "down" || Paused != "paused" {
		t.Fatal("state values must match the schema")
	}
}
