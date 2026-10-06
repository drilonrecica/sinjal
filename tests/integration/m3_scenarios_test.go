package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// M3-13: the incident scenarios of docs/20 that need the whole binary.
// Package tests cover the edge cases at processor level; this proves the
// shipped wiring: scheduler, workers, result processor, incidents, intents
// and the FLAPPING overlay together.

// Scenario 4, flapping detection and suppression: a target that fails
// twice and then succeeds, over and over, makes the monitor go down and
// recover every second or so. The fourth transition within the window sets
// the FLAPPING overlay, announces it once and holds back its own recovery.
func TestFlappingScenario(t *testing.T) {
	skipShort(t)
	var n atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%3 != 0 { // fail, fail (DOWN), succeed (UP), ...
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer target.Close()

	dir := t.TempDir()
	if err := start(t, dir).stop(); err != nil {
		t.Fatal(err)
	}
	d := openDB(t, dir)
	id := seedMonitor(t, d, "flappy", target.URL)
	// A one-second interval (validation allows nothing below ten).
	if _, err := d.Writer.Exec(`UPDATE monitors SET interval_seconds = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	s := start(t, dir)
	waitFor(t, s, "the monitor to flap", func() bool {
		return count(t, d, `SELECT COUNT(*) FROM monitors WHERE id = ? AND flapping_since IS NOT NULL`, id) == 1
	})
	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}

	// Two outages and two recoveries were transitions; the fourth, the
	// second recovery, is the one that tipped it.
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND ended_at IS NOT NULL`, id); n < 2 {
		t.Fatalf("%d ended incidents, want at least 2", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incident_events e JOIN incidents i ON i.id = e.incident_id
		WHERE i.monitor_id = ? AND e.event_type = 'notification_suppressed' AND e.message = 'recovery: flapping'`, id); n < 1 {
		t.Fatal("no recovery was held back by flapping")
	}
	// The log carries the intents in the order they were decided: the
	// first outage and recovery and the second outage are delivered; the
	// fourth transition then decides the one flapping notice, ahead of its
	// own recovery, which it suppresses.
	var kinds []string
	for _, r := range s.records() {
		if r["msg"] == "notification intent" {
			kind, _ := r["kind"].(string)
			if r["suppressed"] != "" {
				kind += "(" + r["suppressed"].(string) + ")"
			}
			kinds = append(kinds, kind)
		}
	}
	got := strings.Join(kinds, " ")
	if !strings.HasPrefix(got, "down recovery down flapping recovery(flapping)") {
		t.Fatalf("intents = %q, want them to start with \"down recovery down flapping recovery(flapping)\"\n%s", got, s.logs)
	}
	if n := strings.Count(" "+got+" ", " flapping "); n != 1 {
		t.Fatalf("%d flapping notices, want 1: %q", n, got)
	}
}
