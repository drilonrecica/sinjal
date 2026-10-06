package results

import (
	"context"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
)

// window stores a one-time maintenance window from second from to second
// to after base, suppressing notifications, for the given monitors (every
// monitor when none are given).
func window(t *testing.T, d *db.DB, from, to int, suppress bool, monitors ...string) {
	t.Helper()
	if _, err := store.CreateMaintenance(context.Background(), d, maintenance.Window{Name: "w", Start: at(from),
		Duration: time.Duration(to-from) * time.Second, Recurrence: maintenance.None, Suppress: suppress,
		Scope: maintenance.Scope{Monitors: monitors}}, base); err != nil {
		t.Fatal(err)
	}
}

func overlaps(t *testing.T, d *db.DB, monitorID string) []bool {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT maintenance_overlap FROM incidents WHERE monitor_id = ? ORDER BY started_at`, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []bool
	for rows.Next() {
		var b bool
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// Inside a window that suppresses notifications the outage is recorded and
// marked, its DOWN held back; the first result after the window, the
// monitor still down, decides it.
func TestMaintenanceHoldsTheDown(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	window(t, d, 0, 120, true)

	h.feed(fail(id, 1), fail(id, 2), fail(id, 60))
	same(t, "intents in the window", h.intentsOf(id), []string{"down 2 maintenance"})
	if got := overlaps(t, d, id); len(got) != 1 || !got[0] {
		t.Fatalf("maintenance_overlap = %v", got)
	}
	h.feed(fail(id, 120), fail(id, 150))
	same(t, "intents after the window", h.intentsOf(id), []string{"down 2 maintenance", "down 120 -"})
	h.feed(ok(id, 180))
	same(t, "intents after the recovery", h.intentsOf(id), []string{"down 2 maintenance", "down 120 -", "recovery 180 -"})
	same(t, "events", events(t, d, id), []string{
		"detected 1 status 503, expected 200-399",
		"declared_down 2 status 503, expected 200-399",
		"notification_suppressed 2 down: maintenance",
		"notification_resumed 120 -",
		"recovered 180 down for 2m59s",
	})
}

// An outage that ends inside a window: its recovery is held back, the
// incident is marked when it closes, and nothing follows the window.
func TestRecoveryInsideMaintenance(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	window(t, d, 100, 200, true)

	h.feed(fail(id, 1), fail(id, 2))
	if got := overlaps(t, d, id); len(got) != 1 || got[0] {
		t.Fatalf("maintenance_overlap at the opening = %v", got)
	}
	h.feed(ok(id, 150), ok(id, 210))
	same(t, "intents", h.intentsOf(id), []string{"down 2 -", "recovery 150 maintenance"})
	if got := overlaps(t, d, id); len(got) != 1 || !got[0] {
		t.Fatalf("maintenance_overlap at the close = %v", got)
	}
}

// An outage that contains a whole window is marked when it closes.
func TestOutageAroundMaintenance(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	window(t, d, 100, 200, true)
	h.feed(fail(id, 1), fail(id, 2), ok(id, 300))
	same(t, "intents", h.intentsOf(id), []string{"down 2 -", "recovery 300 -"})
	if got := overlaps(t, d, id); len(got) != 1 || !got[0] {
		t.Fatalf("maintenance_overlap = %v", got)
	}
}

// A window that does not suppress notifications, one that is over and one
// for another monitor hold nothing back; only the first marks the outage.
func TestMaintenanceThatHoldsNothing(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id, other := newMonitor(t, d, "web", nil), newMonitor(t, d, "other", nil)
	window(t, d, 0, 100, false)
	window(t, d, -100, 0, true)
	window(t, d, 0, 100, true, other)

	h.feed(fail(id, 1), fail(id, 2), ok(id, 3))
	same(t, "intents", h.intentsOf(id), []string{"down 2 -", "recovery 3 -"})
	if got := overlaps(t, d, id); len(got) != 1 || !got[0] {
		t.Fatalf("maintenance_overlap = %v", got)
	}
}

// A daily window keeps its local time in the instance time zone: started
// in March at 14:00 in Belgrade (13:00 UTC), it is at 14:00 CEST, 12:00
// UTC, in October.
func TestMaintenanceInTheInstanceTimeZone(t *testing.T) {
	bel, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		loc  *time.Location
		want string
	}{{bel, "down 2 maintenance"}, {time.UTC, "down 2 -"}} {
		d, _ := testDB(t)
		h := newHarness(t, d)
		h.p.loc = c.loc
		h.run()
		id := newMonitor(t, d, "web", nil)
		if _, err := store.CreateMaintenance(context.Background(), d, maintenance.Window{Name: "nightly",
			Start: time.Date(2026, 3, 20, 14, 0, 0, 0, bel), Duration: 10 * time.Minute,
			Recurrence: maintenance.Daily, Suppress: true}, base); err != nil {
			t.Fatal(err)
		}
		h.feed(fail(id, 1), fail(id, 2))
		same(t, "intents in "+c.loc.String(), h.intentsOf(id), []string{c.want})
	}
}

// The window can end within the batch that opened the incident.
func TestMaintenanceEndsInTheSameBatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	id := newMonitor(t, d, "web", nil)
	window(t, d, 0, 100, true)
	for _, r := range []Result{fail(id, 1), fail(id, 2), fail(id, 100), fail(id, 130)} {
		h.p.Add(context.Background(), r)
	}
	h.run()
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 4 })
	same(t, "intents", h.intentsOf(id), []string{"down 2 maintenance", "down 100 -"})
}
