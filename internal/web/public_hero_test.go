package web

import (
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The status sentence (M8-17): the overall state in words, naming the
// services that are down by their public names.
func TestPublicSentence(t *testing.T) {
	rows := func(names ...string) []templates.PublicGroup {
		var g templates.PublicGroup
		for _, n := range names {
			g.Rows = append(g.Rows, templates.PublicRow{Name: n, State: templates.StateDown})
		}
		g.Rows = append(g.Rows, templates.PublicRow{Name: "Fine", State: templates.StateUp})
		return []templates.PublicGroup{g}
	}
	for _, tt := range []struct {
		overall, text string
		groups        []templates.PublicGroup
		want          string
	}{
		{templates.OverallUp, "All systems operational", rows(), "All systems operational."},
		{templates.OverallDegraded, "Degraded performance", rows(), "Degraded performance."},
		{templates.OverallMaintenance, "Under maintenance", rows(), "Under maintenance."},
		{templates.OverallNone, "No services are monitored", nil, "No services are monitored."},
		{templates.OverallPartial, "Partial outage", rows("Database"), "Partial outage: Database is down."},
		{templates.OverallPartial, "Partial outage", rows("Database", "Web"), "Partial outage: Database and Web are down."},
		{templates.OverallMajor, "Major outage", rows("A", "B", "C"), "Major outage: A, B and C are down."},
		{templates.OverallMajor, "Major outage", rows("A", "B", "C", "D", "E"), "Major outage: A, B, C and 2 more are down."},
	} {
		if got := publicSentence(tt.overall, tt.text, tt.groups); got != tt.want {
			t.Errorf("%s %v: %q, want %q", tt.overall, tt.groups, got, tt.want)
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
