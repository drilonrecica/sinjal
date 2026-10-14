package web

import (
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
)

func TestActivityLines(t *testing.T) {
	now := time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC)
	checks := []recent.Entry{ // newest first, as Feed gives them
		{MonitorID: "a", Check: recent.Check{At: now.Add(-10 * time.Second).Unix(), MS: 128, OK: true}},
		{MonitorID: "gone", Check: recent.Check{At: now.Add(-20 * time.Second).Unix(), OK: true}},
		{MonitorID: "b", Check: recent.Check{At: now.Add(-40 * time.Second).Unix(), MS: 5000, Kind: httpcheck.KindTimeout}},
	}
	events := []store.RecentEvent{{IncidentID: "i1", MonitorID: "b", MonitorName: "Billing", Type: "declared_down", At: now.Add(-30 * time.Second)}}
	got := activityLinesOf(checks, events, map[string]string{"a": "API", "b": "Billing"}, now, time.UTC)
	want := []string{
		"up API /monitors/a Up 128 ms 17:59:50",
		"event Billing /incidents/i1 Declared down  17:59:30",
		"down Billing /monitors/b Down: Timeout 5.0 s 17:59:20",
	}
	if len(got) != len(want) {
		t.Fatalf("%d lines: %+v", len(got), got)
	}
	for i, l := range got {
		if s := l.State + " " + l.Monitor + " " + l.Href + " " + l.What + " " + l.Detail + " " + l.Time; s != want[i] {
			t.Errorf("line %d = %q, want %q", i, s, want[i])
		}
	}
}
