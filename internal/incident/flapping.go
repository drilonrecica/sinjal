package incident

import "time"

// Flapping policy (docs/10_INCIDENTS.md, decision P0-08): fixed in v1.
const (
	// FlapTransitions confirmed transitions within FlapWindow start the
	// FLAPPING overlay; FlapWindow without one ends it.
	FlapTransitions = 4
	FlapWindow      = 10 * time.Minute
)

// FlapStarts reports whether a monitor starts flapping at now, given the
// times of its recent confirmed transitions (an incident opening or a
// check closing one), the one happening at now included. A transition
// counts while it is less than FlapWindow old.
func FlapStarts(transitions []time.Time, now time.Time) bool {
	n := 0
	for _, t := range transitions {
		if now.Sub(t) < FlapWindow {
			n++
		}
	}
	return n >= FlapTransitions
}

// FlapEnded reports whether a flapping monitor whose last transition was
// at last has been quiet for FlapWindow at now.
func FlapEnded(last, now time.Time) bool {
	return now.Sub(last) >= FlapWindow
}
