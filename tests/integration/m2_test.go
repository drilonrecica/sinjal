package integration

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M2: the monitoring engine in the real binary.

// seedMonitor creates an enabled HTTP monitor for url in a stopped server's
// database: thresholds 2 and 1, a 20 ms retry delay.
func seedMonitor(t *testing.T, d *db.DB, name, url string) string {
	t.Helper()
	id, err := store.CreateHTTPMonitor(context.Background(), d, store.HTTPMonitor{
		Name: name, Enabled: true, RetryDelayMS: 20,
		Config: store.HTTPConfig{URL: url, FollowRedirects: true, TLSExpiryEnabled: true},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func monitorRow(t *testing.T, d *db.DB, id string) store.Monitor {
	t.Helper()
	m, err := store.GetMonitor(context.Background(), d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// queryString returns the single text value a query selects.
func queryString(t *testing.T, d *db.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := d.Reader.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, s *server, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n%s", what, s.logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRestartKeepsMonitorState: a stored monitor is checked as soon as the
// server starts; an outage survives a restart as the same outage (DOWN
// since the original moment, checked again promptly); and the monitor
// recovers once the target does. Every stop is a clean exit.
func TestRestartKeepsMonitorState(t *testing.T) {
	skipShort(t)
	var failing atomic.Bool
	failing.Store(true)
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer target.Close()

	dir := t.TempDir()
	stop := func(s *server) {
		t.Helper()
		if err := s.stop(); err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
		}
	}
	stop(start(t, dir)) // creates and migrates the database
	d := openDB(t, dir)
	id := seedMonitor(t, d, "api", target.URL)

	// Run 1: the first check fails, the retry confirms, the monitor is DOWN.
	s := start(t, dir)
	waitFor(t, s, "the monitor to be down", func() bool { return monitorRow(t, d, id).State == "down" })
	stop(s)
	down := monitorRow(t, d, id)
	results := count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id)
	if results != 2 || down.LastFailureAt == nil || down.LastSuccessAt != nil {
		t.Fatalf("after run 1: %d results, %+v", results, down)
	}
	// The outage is one active incident, from the first failed check.
	var incidentID, startedAt string
	if err := d.Reader.QueryRow(`SELECT id, started_at FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, id).Scan(&incidentID, &startedAt); err != nil {
		t.Fatalf("no active incident after run 1: %v", err)
	}
	if first := queryString(t, d, `SELECT MIN(checked_at) FROM check_results WHERE monitor_id = ?`, id); startedAt != first {
		t.Fatalf("the incident starts at %s, the first failure was at %s", startedAt, first)
	}

	// Run 2: still failing. A fresh check runs promptly; the outage keeps
	// its start.
	begin := time.Now()
	s = start(t, dir)
	waitFor(t, s, "a check after the restart", func() bool {
		return count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id) > results
	})
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("the first check after a restart took %v", took)
	}
	stop(s)
	if m := monitorRow(t, d, id); m.State != "down" || !m.StateSince.Equal(down.StateSince) {
		t.Fatalf("after run 2: state %s since %v, want down since %v", m.State, m.StateSince, down.StateSince)
	}
	// Scenario 8: the restart neither duplicated nor closed the incident.
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ?`, id); n != 1 {
		t.Fatalf("%d incidents after the restart, want 1", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE id = ? AND ended_at IS NULL AND started_at = ?`, incidentID, startedAt); n != 1 {
		t.Fatal("the incident did not survive the restart unchanged")
	}

	// Run 3: the target is back.
	failing.Store(false)
	s = start(t, dir)
	waitFor(t, s, "the monitor to recover", func() bool { return monitorRow(t, d, id).State == "up" })
	stop(s)
	if m := monitorRow(t, d, id); m.LastSuccessAt == nil || m.StateSince.Before(down.StateSince) {
		t.Fatalf("after run 3: %+v", m)
	}
	// The recovery closed that same incident; its timeline is complete.
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ?`, id); n != 1 {
		t.Fatalf("%d incidents after the recovery, want 1", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE id = ? AND ended_at >= started_at`, incidentID); n != 1 {
		t.Fatal("the incident was not closed by the recovery")
	}
	if got := queryString(t, d, `SELECT group_concat(event_type, ' ') FROM (SELECT event_type FROM incident_events WHERE incident_id = ? ORDER BY id)`, incidentID); got != "detected declared_down recovered" {
		t.Fatalf("incident events: %s", got)
	}
	if !hasMsg(s.records(), "monitoring started") {
		t.Errorf("no \"monitoring started\" log line\n%s", s.logs)
	}
}

// TestEventsStream: a signed-in browser on GET /events is told when a
// monitor's check has been stored, nobody else gets the stream, and a
// shutdown ends an open stream cleanly and at once instead of waiting out
// the grace period.
func TestEventsStream(t *testing.T) {
	skipShort(t)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()

	dir := t.TempDir()
	s := start(t, dir)
	createAdmin(t, s)
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	d := openDB(t, dir)
	id := seedMonitor(t, d, "api", target.URL)
	// Checked every two seconds, so that events keep coming after the
	// stream is open (validation allows nothing below ten).
	if _, err := d.Writer.Exec(`UPDATE monitors SET interval_seconds = 2 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	s = start(t, dir)
	resp, err := noRedirect.Get(s.base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /events signed out = %d, want a redirect to the login", resp.StatusCode)
	}

	c := signIn(t, s, "admin", adminPassword)
	req, _ := http.NewRequest(http.MethodGet, s.base+"/events", nil)
	req.AddCookie(&http.Cookie{Name: "sinjal_session", Value: c.token})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /events = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	lines := make(chan string, 256)
	ended := make(chan error, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ended <- err
				return
			}
			lines <- strings.TrimRight(line, "\n")
		}
	}()
	// The frame of the next stored check: event name, then the monitor id.
	want := []string{"event: monitor.updated", `data: {"monitor_id":"` + id + `"}`}
	deadline := time.After(15 * time.Second)
	for found := 0; found < len(want); {
		select {
		case line := <-lines:
			switch {
			case line == want[found]:
				found++
			case found > 0:
				t.Fatalf("after %q came %q, want %q", want[found-1], line, want[found])
			}
		case err := <-ended:
			t.Fatalf("the stream ended early: %v\n%s", err, s.logs)
		case <-deadline:
			t.Fatalf("no monitor.updated event within 15 s\n%s", s.logs)
		}
	}

	// Shutdown with the stream open.
	begin := time.Now()
	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}
	// Well inside the 10 s grace, which an unclosed stream would use up
	// (and log as timed out, checked below). Not tighter: net/http's
	// Shutdown counts a connection that never sent a request as idle only
	// once it is 5 s old, and Go's client sometimes dials such a spare
	// connection while racing for an idle one.
	if took := time.Since(begin); took > 9*time.Second {
		t.Errorf("shutdown with an open stream took %v", took)
	}
	select {
	case err := <-ended:
		if err != io.EOF {
			t.Errorf("the stream ended with %v, want a clean end", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the stream was not closed by the shutdown")
	}
	if hasMsg(s.records(), "graceful shutdown timed out; closing remaining connections") {
		t.Errorf("the shutdown had to cut connections\n%s", s.logs)
	}
}
