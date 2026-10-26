package web

import (
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The headline (docs/12 "Headline"): the services that are down, unstable
// or under maintenance by their public names, and the verb apart so a
// layout can colour it; nothing when everything is up or nothing is
// monitored.
func TestPublicHeadline(t *testing.T) {
	rows := func(state string, names ...string) []templates.PublicGroup {
		var g templates.PublicGroup
		for _, n := range names {
			g.Rows = append(g.Rows, templates.PublicRow{Name: n, State: state})
		}
		g.Rows = append(g.Rows, templates.PublicRow{Name: "Fine", State: templates.StateUp})
		return []templates.PublicGroup{g}
	}
	for _, tt := range []struct {
		overall     string
		groups      []templates.PublicGroup
		names, verb string
	}{
		{templates.OverallUp, rows(templates.StateUp), "", ""},
		{templates.OverallNone, nil, "", ""},
		{templates.OverallPartial, rows(templates.StateDown, "Database"), "Database", "is down"},
		{templates.OverallPartial, rows(templates.StateDown, "Database", "Web"), "Database and Web", "are down"},
		{templates.OverallMajor, rows(templates.StateDown, "A", "B", "C"), "A, B and C", "are down"},
		{templates.OverallMajor, rows(templates.StateDown, "A", "B", "C", "D", "E"), "A, B, C and 2 more", "are down"},
		{templates.OverallDegraded, rows(templates.StateFlapping, "Checkout"), "Checkout", "is unstable"},
		{templates.OverallDegraded, append(rows(templates.StatePending, "New"), rows(templates.StateFlapping, "Checkout")...), "New and Checkout", "are unstable"},
		{templates.OverallMaintenance, rows(templates.StateMaintenance, "Checkout"), "Checkout", "is under maintenance"},
	} {
		if names, verb := publicHeadline(tt.overall, tt.groups); names != tt.names || verb != tt.verb {
			t.Errorf("%s %v: %q %q, want %q %q", tt.overall, tt.groups, names, verb, tt.names, tt.verb)
		}
	}
}

func TestPublicHeroState(t *testing.T) {
	for overall, want := range map[string]string{
		templates.OverallMajor: "down", templates.OverallPartial: "down", templates.OverallDegraded: "flapping",
		templates.OverallMaintenance: "up", templates.OverallUp: "up", templates.OverallNone: "up",
	} {
		if got := publicHeroState(overall); got != want {
			t.Errorf("%s: %q, want %q", overall, got, want)
		}
	}
}

// The note under the sentence is the newest published note of an ongoing
// incident; an ended incident's notes stay with it below.
func TestLatestPublicNote(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return now.Add(time.Duration(-m) * time.Minute) }
	ended := now.Add(-time.Hour)
	incidents := []store.PageIncident{
		{ID: "old", DisplayName: "Web", EndedAt: &ended, Notes: []store.PageNote{{Message: "fixed", At: at(70)}}},
		{ID: "a", DisplayName: "Database", Notes: []store.PageNote{{Message: "looking", At: at(30)}, {Message: "failover running", At: at(10)}}},
		{ID: "b", DisplayName: "Web", Notes: []store.PageNote{{Message: "slow", At: at(20)}}},
	}
	n, ok := latestPublicNote(incidents)
	if !ok || n.Message != "failover running" || n.Service != "Database" || !n.At.Equal(at(10)) {
		t.Fatalf("%+v %v", n, ok)
	}
	if _, ok := latestPublicNote(incidents[:1]); ok {
		t.Fatal("an ended incident's note is shown under the sentence")
	}
}
