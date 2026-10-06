package results

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/store"
)

// intentList returns the intents handed out so far, each as
// "kind time reason" with the time in seconds after base and "-" for an
// intent that is to be delivered.
func (h *harness) intentList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, in := range h.intents {
		reason := string(in.Suppressed)
		if reason == "" {
			reason = "-"
		}
		out = append(out, fmt.Sprintf("%s %d %s", in.Kind, int(in.At.Sub(base)/time.Second), reason))
	}
	return out
}

func (h *harness) intent(i int) incident.Intent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.intents[i]
}

// suppressions returns the notification_suppressed events of a monitor's
// incidents.
func suppressions(t *testing.T, d *db.DB, monitorID string) int {
	t.Helper()
	var n int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM incident_events e JOIN incidents i ON i.id = e.incident_id
		WHERE i.monitor_id = ? AND e.event_type = ?`, monitorID, incident.EventNotificationSuppressed).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// One down intent when the incident opens, one recovery intent when a
// check closes it, both about that incident and both to be delivered.
func TestIntentsForAnOutage(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	other := newMonitor(t, d, "other", nil)

	h.feed(ok(id, 1), fail(id, 2), ok(id, 3), ok(other, 3), fail(id, 4))
	same(t, "intents before any outage", h.intentList(), nil)

	h.feed(fail(id, 5))
	same(t, "intents once down", h.intentList(), []string{"down 5 -"})
	var incidentID string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ?`, id).Scan(&incidentID); err != nil {
		t.Fatal(err)
	}
	if in := h.intent(0); in.MonitorID != id || in.IncidentID != incidentID {
		t.Fatalf("down intent %+v, want monitor %s incident %s", in, id, incidentID)
	}

	h.feed(fail(id, 30), fail(id, 60))
	same(t, "intents while down", h.intentList(), []string{"down 5 -"})

	h.feed(ok(id, 90), ok(id, 120))
	same(t, "intents after recovery", h.intentList(), []string{"down 5 -", "recovery 90 -"})
	if in := h.intent(1); in.MonitorID != id || in.IncidentID != incidentID {
		t.Fatalf("recovery intent %+v, want monitor %s incident %s", in, id, incidentID)
	}
	if n := suppressions(t, d, id); n != 0 {
		t.Fatalf("%d suppression events for delivered intents", n)
	}
}

// While the FLAPPING overlay is set the incident is recorded as usual, but
// its down and recovery intents are suppressed and each decision is kept
// on the incident's timeline.
func TestIntentsSuppressedWhileFlapping(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	// Flapping since a recovery at base.
	if _, err := d.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES ('earlier', ?, ?, ?, ?)`,
		id, store.FormatTime(at(-5)), store.FormatTime(base), store.FormatTime(at(-5))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`UPDATE monitors SET flapping_since = ? WHERE id = ?`, store.FormatTime(base), id); err != nil {
		t.Fatal(err)
	}

	h.feed(fail(id, 1), fail(id, 2), ok(id, 3))
	same(t, "intents", h.intentList(), []string{"down 2 flapping", "recovery 3 flapping"})
	same(t, "incidents", incidents(t, d, id), []string{"-5 0 - -", "1 3 http_status status 503, expected 200-399"})
	same(t, "events", events(t, d, id), []string{
		"detected 1 status 503, expected 200-399",
		"declared_down 2 status 503, expected 200-399",
		"notification_suppressed 2 down: flapping",
		"recovered 3 down for 2s",
		"notification_suppressed 3 recovery: flapping",
	})
}

// Intents leave the processor only once their batch is committed, and once
// however often the write was attempted.
func TestIntentsOnlyAfterTheCommit(t *testing.T) {
	d, path := testDB(t)
	if _, err := d.Writer.Exec(`PRAGMA busy_timeout = 5`); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	lock, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	h.p.Add(ctx, fail(id, 1))
	h.p.Add(ctx, fail(id, 2))
	h.wait("the write to fail", func(s Stats) bool { return s.FailedFlushes >= 1 })
	same(t, "intents while the write fails", h.intentList(), nil)
	if _, err := lock.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	h.wait("the held batch", func(s Stats) bool { return s.Persisted == 2 })
	same(t, "intents", h.intentList(), []string{"down 2 -"})
}

// What does not open or close an incident announces nothing: a restart
// during an outage (no second DOWN), a pause (no recovery), and a monitor
// whose incident was already there.
func TestNoIntentWithoutAnIncidentChange(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	ctx := context.Background()
	id := newMonitor(t, d, "web", nil)
	h.feed(fail(id, 1), fail(id, 2))
	h.stop()
	same(t, "intents before the restart", h.intentList(), []string{"down 2 -"})

	h = newHarness(t, d).run()
	h.feed(fail(id, 30), fail(id, 31))
	same(t, "intents after a restart while down", h.intentList(), nil)

	if _, err := store.PauseMonitor(ctx, d, id, at(40)); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(id, 41))
	same(t, "intents after a pause", h.intentList(), nil)

	double := newMonitor(t, d, "double", nil)
	if _, err := d.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES ('stale', ?, ?, ?)`,
		double, store.FormatTime(base), store.FormatTime(base)); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(double, 50), fail(double, 51))
	same(t, "intents for an incident that already existed", h.intentList(), nil)
}

// Every intent is logged, so a decision can be followed without a
// dispatcher.
func TestIntentsAreLogged(t *testing.T) {
	d, _ := testDB(t)
	var buf bytes.Buffer
	p := New(d, slog.New(slog.NewTextHandler(&buf, nil)), time.UTC, nil, nil, nil)
	p.flushAfter = time.Millisecond
	id := newMonitor(t, d, "web", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	p.Add(ctx, fail(id, 1))
	p.Add(ctx, fail(id, 2))
	for p.Stats().Persisted < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	var line string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `msg="notification intent"`) {
			line += l + "\n"
		}
	}
	if strings.Count(line, "\n") != 1 || !strings.Contains(line, "kind=down") || !strings.Contains(line, "monitor_id="+id) ||
		!strings.Contains(line, `suppressed=""`) || !strings.Contains(line, "incident_id=") {
		t.Fatalf("intent log lines:\n%s\nall:\n%s", line, buf.String())
	}
}
