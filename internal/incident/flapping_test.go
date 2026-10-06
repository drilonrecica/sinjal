package incident

import (
	"testing"
	"time"
)

func TestFlapStarts(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ago := func(seconds ...int) []time.Time {
		var out []time.Time
		for _, s := range seconds {
			out = append(out, now.Add(-time.Duration(s)*time.Second))
		}
		return out
	}
	cases := []struct {
		name        string
		transitions []time.Time
		want        bool
	}{
		{"none", nil, false},
		{"one, now", ago(0), false},
		{"three", ago(0, 10, 20), false},
		{"four", ago(0, 10, 20, 30), true},
		{"four at the same moment", ago(0, 0, 0, 0), true},
		{"more than four", ago(0, 10, 20, 30, 40, 50), true},
		{"four, the oldest just inside the window", ago(0, 100, 200, 599), true},
		{"four, the oldest exactly a window old", ago(0, 100, 200, 600), false},
		{"four, one long ago", ago(0, 10, 20, 3600), false},
		{"five, two outside the window", ago(0, 10, 20, 600, 900), false},
		{"order does not matter", ago(599, 20, 0, 10), true},
	}
	for _, tc := range cases {
		if got := FlapStarts(tc.transitions, now); got != tc.want {
			t.Errorf("%s: FlapStarts = %v, want %v", tc.name, got, tc.want)
		}
	}
	if FlapTransitions != 4 || FlapWindow != 10*time.Minute {
		t.Errorf("policy is %d transitions in %v; docs/10 says 4 in 10 minutes", FlapTransitions, FlapWindow)
	}
}

func TestFlapEnded(t *testing.T) {
	last := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for after, want := range map[time.Duration]bool{
		0:                                false,
		time.Minute:                      false,
		10*time.Minute - time.Second:     false,
		10 * time.Minute:                 true,
		time.Hour:                        true,
		-time.Minute:                     false, // a result older than the transition
		10*time.Minute + time.Second:     true,
		10*time.Minute - time.Nanosecond: false,
	} {
		if got := FlapEnded(last, last.Add(after)); got != want {
			t.Errorf("FlapEnded after %v = %v, want %v", after, got, want)
		}
	}
	// No known transition at all: nothing keeps the overlay up.
	if !FlapEnded(time.Time{}, last) {
		t.Error("FlapEnded with no transition must be true")
	}
}
