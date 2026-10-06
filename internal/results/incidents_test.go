package results

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// sec renders a stored timestamp as seconds after base, "-" when NULL.
func sec(t *testing.T, s sql.NullString) string {
	t.Helper()
	if !s.Valid {
		return "-"
	}
	v, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(int(v.Sub(base) / time.Second))
}

// incidents returns a monitor's incidents, oldest first, each as
// "start end kind summary" with times in seconds after base.
func incidents(t *testing.T, d *db.DB, monitorID string) []string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT started_at, ended_at, COALESCE(initial_failure_kind, '-'), COALESCE(summary, '-')
		FROM incidents WHERE monitor_id = ? ORDER BY started_at, created_at`, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var start, end sql.NullString
		var kind, summary string
		if err := rows.Scan(&start, &end, &kind, &summary); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join([]string{sec(t, start), sec(t, end), kind, summary}, " "))
	}
	return out
}

// events returns the timelines of a monitor's incidents in the order they
// were written, each entry as "type time message".
func events(t *testing.T, d *db.DB, monitorID string) []string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT e.event_type, e.created_at, COALESCE(e.message, '-') FROM incident_events e
		JOIN incidents i ON i.id = e.incident_id WHERE i.monitor_id = ? ORDER BY e.id`, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ, msg string
		var at sql.NullString
		if err := rows.Scan(&typ, &at, &msg); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join([]string{typ, sec(t, at), msg}, " "))
	}
	return out
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %q\n want %q", what, got, want)
	}
}

// failWith is a failed check with its own kind and message.
func failWith(id string, n int, kind, message string) Result {
	return Result{MonitorID: id, CheckedAt: at(n), Kind: kind, Message: message}
}

// An incident opens when the failure threshold is met and starts at the
// first failure of that run of failures; it closes at the recovering check.
func TestIncidentLifecycle(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)

	h.feed(ok(id, 1), failWith(id, 2, "timeout", "timeout after 5s"))
	same(t, "incidents while pending", incidents(t, d, id), nil)

	h.feed(failWith(id, 4, "connect", "connection refused"))
	same(t, "incidents once down", incidents(t, d, id), []string{"2 - timeout connection refused"})
	same(t, "events once down", events(t, d, id), []string{
		"detected 2 timeout after 5s",
		"declared_down 4 connection refused",
	})
	var created string
	if err := d.Reader.QueryRow(`SELECT created_at FROM incidents WHERE monitor_id = ?`, id).Scan(&created); err != nil || created != store.FormatTime(at(4)) {
		t.Fatalf("created_at = %s (%v), want the confirming check", created, err)
	}

	// Staying down changes nothing.
	h.feed(fail(id, 30), fail(id, 60))
	same(t, "incidents while down", incidents(t, d, id), []string{"2 - timeout connection refused"})
	if n := len(events(t, d, id)); n != 2 {
		t.Fatalf("%d events while down, want 2", n)
	}

	h.feed(ok(id, 259))
	same(t, "incidents after recovery", incidents(t, d, id), []string{"2 259 timeout connection refused"})
	same(t, "events after recovery", events(t, d, id)[2:], []string{"recovered 259 down for 4m17s"})

	// The next outage is a new incident.
	h.feed(fail(id, 300), fail(id, 301))
	same(t, "incidents after a second outage", incidents(t, d, id), []string{
		"2 259 timeout connection refused",
		"300 - http_status status 503, expected 200-399",
	})
}

// With a failure threshold of one the first failure is the outage.
func TestIncidentWithThresholdOne(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", func(m *store.MonitorInput) { m.FailureThreshold = 1 })
	h.feed(ok(id, 1), failWith(id, 2, "dns", "no such host"))
	same(t, "incidents", incidents(t, d, id), []string{"2 - dns no such host"})
	same(t, "events", events(t, d, id), []string{"detected 2 no such host", "declared_down 2 no such host"})
}

// A failure that recovers on the confirmation retry is no incident, and it
// is not the start of a later one.
func TestBlipOpensNoIncident(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(ok(id, 1), fail(id, 2), ok(id, 3))
	same(t, "incidents after a blip", incidents(t, d, id), nil)
	h.feed(failWith(id, 10, "timeout", "slow"), fail(id, 11))
	same(t, "incidents", incidents(t, d, id), []string{"10 - timeout status 503, expected 200-399"})
}

// The incident stays open until the success threshold is met and closes at
// the check that meets it.
func TestIncidentClosesAtTheSuccessThreshold(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", func(m *store.MonitorInput) { m.FailureThreshold, m.SuccessThreshold = 3, 2 })
	h.feed(fail(id, 1), fail(id, 2), fail(id, 3))
	same(t, "incidents", incidents(t, d, id), []string{"1 - http_status status 503, expected 200-399"})
	h.feed(ok(id, 4), fail(id, 5), ok(id, 6))
	same(t, "incidents before the second success in a row", incidents(t, d, id), []string{"1 - http_status status 503, expected 200-399"})
	h.feed(ok(id, 7))
	same(t, "incidents", incidents(t, d, id), []string{"1 7 http_status status 503, expected 200-399"})
}

// Two outages of one monitor inside a single batch are two incidents.
func TestIncidentsWithinOneBatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.flushAfter, h.p.batchMax = time.Hour, 6
	h.run()
	a, b := newMonitor(t, d, "a", nil), newMonitor(t, d, "b", nil)
	for _, r := range []Result{fail(a, 1), fail(a, 2), ok(b, 2), ok(a, 3), fail(a, 4), fail(a, 5)} {
		h.p.Add(context.Background(), r)
	}
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 6 })
	same(t, "incidents of a", incidents(t, d, a), []string{
		"1 3 http_status status 503, expected 200-399",
		"4 - http_status status 503, expected 200-399",
	})
	same(t, "events of a", events(t, d, a), []string{
		"detected 1 status 503, expected 200-399", "declared_down 2 status 503, expected 200-399",
		"recovered 3 down for 2s",
		"detected 4 status 503, expected 200-399", "declared_down 5 status 503, expected 200-399",
	})
	same(t, "incidents of b", incidents(t, d, b), nil)
}

// A batch whose write failed is applied again from the top: the incident
// it opens exists once, however often the transaction was attempted.
func TestIncidentAfterAFailedWrite(t *testing.T) {
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
	if _, err := lock.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	h.wait("the held batch", func(s Stats) bool { return s.Persisted == 2 })
	same(t, "incidents", incidents(t, d, id), []string{"1 - http_status status 503, expected 200-399"})
	if n := len(events(t, d, id)); n != 2 {
		t.Fatalf("%d events, want 2", n)
	}
}

// Scenario 8 of docs/20 at processor level: a restart during an outage
// keeps the one incident, and the recovery after it closes that incident.
func TestRestartWithActiveIncident(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(fail(id, 1), fail(id, 2))
	h.stop()
	var first string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ?`, id).Scan(&first); err != nil {
		t.Fatal(err)
	}

	h = newHarness(t, d).run() // a new process: nothing is kept in memory
	h.feed(fail(id, 60), fail(id, 61), fail(id, 90))
	same(t, "incidents after the restart", incidents(t, d, id), []string{"1 - http_status status 503, expected 200-399"})
	h.feed(ok(id, 120))
	same(t, "incidents after recovery", incidents(t, d, id), []string{"1 120 http_status status 503, expected 200-399"})
	var closed string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ?`, id).Scan(&closed); err != nil || closed != first {
		t.Fatalf("the incident changed identity across the restart: %s, %s (%v)", first, closed, err)
	}
	same(t, "events", events(t, d, id), []string{
		"detected 1 status 503, expected 200-399", "declared_down 2 status 503, expected 200-399",
		"recovered 120 down for 1m59s",
	})
}

// A restart while pending forgets the failures counted so far (docs/09):
// the outage, if it comes, starts at the first failure after the restart.
func TestRestartWhilePending(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(ok(id, 1), fail(id, 2))
	h.stop()
	h = newHarness(t, d).run()
	h.feed(fail(id, 30))
	same(t, "incidents after one failure", incidents(t, d, id), nil)
	h.feed(fail(id, 31))
	same(t, "incidents", incidents(t, d, id), []string{"30 - http_status status 503, expected 200-399"})
}

// Pausing ends the outage (store.PauseMonitor); what fails after a resume
// is a new incident.
func TestPauseClosesTheIncident(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	ctx := context.Background()
	id := newMonitor(t, d, "web", nil)
	h.feed(fail(id, 1), fail(id, 2))
	if _, err := store.PauseMonitor(ctx, d, id, at(40)); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(id, 41)) // a check that was already running
	same(t, "incidents after the pause", incidents(t, d, id), []string{"1 40 http_status status 503, expected 200-399"})
	same(t, "events after the pause", events(t, d, id)[2:], []string{"paused 40 down for 39s"})

	if _, err := store.ResumeMonitor(ctx, d, id, at(100)); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(id, 101))
	same(t, "incidents after one failure", incidents(t, d, id), []string{"1 40 http_status status 503, expected 200-399"})
	h.feed(fail(id, 102))
	same(t, "incidents", incidents(t, d, id), []string{
		"1 40 http_status status 503, expected 200-399",
		"101 - http_status status 503, expected 200-399",
	})
}

// Rows that disagree with the rule "down means one active incident" (an
// edit behind the processor's back) must not stop the pipeline: a held
// batch would block every monitor.
func TestInconsistentRowsDoNotBlock(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	orphan, double := newMonitor(t, d, "orphan", nil), newMonitor(t, d, "double", nil)
	// Down without an incident.
	if _, err := d.Writer.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, orphan); err != nil {
		t.Fatal(err)
	}
	// Up with an incident that was never closed.
	if _, err := d.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES ('stale', ?, ?, ?)`,
		double, store.FormatTime(base), store.FormatTime(base)); err != nil {
		t.Fatal(err)
	}
	h.feed(ok(orphan, 1), fail(double, 1), fail(double, 2))
	if s := h.p.Stats(); s.Persisted != 3 || s.Warning != "" || s.FailedFlushes != 0 {
		t.Fatalf("stats: %+v", s)
	}
	if h.monitor(orphan).State != "up" || h.monitor(double).State != "down" {
		t.Fatal("states were not applied")
	}
	same(t, "incidents of orphan", incidents(t, d, orphan), nil)
	same(t, "incidents of double", incidents(t, d, double), []string{"0 - - -"})
	h.feed(ok(double, 3))
	same(t, "incidents of double after recovery", incidents(t, d, double), []string{"0 3 - -"})
}
