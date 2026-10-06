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

	// Run 3: the target is back.
	failing.Store(false)
	s = start(t, dir)
	waitFor(t, s, "the monitor to recover", func() bool { return monitorRow(t, d, id).State == "up" })
	stop(s)
	if m := monitorRow(t, d, id); m.LastSuccessAt == nil || m.StateSince.Before(down.StateSince) {
		t.Fatalf("after run 3: %+v", m)
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
	if took := time.Since(begin); took > 5*time.Second {
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
