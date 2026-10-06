package results

import (
	"context"
	"fmt"
	"testing"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// child creates a monitor that depends on parent.
func child(t *testing.T, d *db.DB, name, parent string) string {
	t.Helper()
	return newMonitor(t, d, name, func(m *store.HTTPMonitor) { m.ParentMonitorID = parent })
}

// intentsOf is intentList for one monitor.
func (h *harness) intentsOf(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, in := range h.intents {
		if in.MonitorID != id {
			continue
		}
		reason := string(in.Suppressed)
		if reason == "" {
			reason = "-"
		}
		out = append(out, fmt.Sprintf("%s %d %s", in.Kind, int(in.At.Sub(base).Seconds()), reason))
	}
	return out
}

func suppressedByParent(t *testing.T, d *db.DB, monitorID string) []bool {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT suppressed_by_parent FROM incidents WHERE monitor_id = ? ORDER BY started_at`, monitorID)
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

// While the parent is down the child's outage is recorded as usual and its
// DOWN is held back; once the parent has recovered, the child, still down,
// announces it on its next result, once.
func TestParentDownHoldsTheChildsDown(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)

	h.feed(fail(p, 1), fail(p, 2), fail(c, 3), fail(c, 4))
	same(t, "parent intents", h.intentsOf(p), []string{"down 2 -"})
	same(t, "child intents", h.intentsOf(c), []string{"down 4 parent"})
	same(t, "child incidents", incidents(t, d, c), []string{"3 - http_status status 503, expected 200-399"})
	if got := suppressedByParent(t, d, c); len(got) != 1 || !got[0] {
		t.Fatalf("suppressed_by_parent = %v", got)
	}

	h.feed(fail(c, 30))
	same(t, "child intents while the parent is down", h.intentsOf(c), []string{"down 4 parent"})

	h.feed(ok(p, 40), fail(c, 60), fail(c, 90))
	same(t, "child intents after the parent recovered", h.intentsOf(c), []string{"down 4 parent", "down 60 -"})

	h.feed(ok(c, 120))
	same(t, "child intents after its recovery", h.intentsOf(c), []string{"down 4 parent", "down 60 -", "recovery 120 -"})
	same(t, "child events", events(t, d, c), []string{
		"detected 3 status 503, expected 200-399",
		"declared_down 4 status 503, expected 200-399",
		"notification_suppressed 4 down: parent",
		"notification_resumed 60 -",
		"recovered 120 down for 1m57s",
	})
}

// A child that recovers before its parent has nothing left to announce:
// its recovery is held back too, and no DOWN follows the parent's
// recovery.
func TestChildRecoversWhileTheParentIsDown(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)

	h.feed(fail(p, 1), fail(p, 2), fail(c, 3), fail(c, 4), ok(c, 5), ok(p, 6), ok(c, 30))
	same(t, "child intents", h.intentsOf(c), []string{"down 4 parent", "recovery 5 parent"})
	same(t, "parent intents", h.intentsOf(p), []string{"down 2 -", "recovery 6 -"})
}

// A parent that is only pending holds nothing back, and an outage of the
// child that began without the parent being down is not marked.
func TestPendingParentHoldsNothing(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)

	h.feed(fail(p, 1), fail(c, 2), fail(c, 3))
	same(t, "child intents", h.intentsOf(c), []string{"down 3 -"})
	if got := suppressedByParent(t, d, c); len(got) != 1 || got[0] {
		t.Fatalf("suppressed_by_parent = %v", got)
	}
	// The parent goes down after the child: the child's recovery is held.
	h.feed(fail(p, 4), ok(c, 5))
	same(t, "child intents", h.intentsOf(c), []string{"down 3 -", "recovery 5 parent"})
}

// The parent's state is read inside the batch: a parent that goes down
// earlier in the same batch holds back the child's DOWN.
func TestParentAndChildInOneBatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)
	for _, r := range []Result{fail(p, 1), fail(c, 1), fail(p, 2), fail(c, 2)} {
		h.p.Add(context.Background(), r)
	}
	h.run()
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 4 })
	same(t, "child intents", h.intentsOf(c), []string{"down 2 parent"})
}

// What is held back is read from the incident's timeline, so the DOWN is
// announced after a restart as well, and only once.
func TestHeldDownAcrossARestart(t *testing.T) {
	d, _ := testDB(t)
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)
	h := newHarness(t, d).run()
	h.feed(fail(p, 1), fail(p, 2), fail(c, 3), fail(c, 4))
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(c, 30))
	same(t, "child intents while the parent is still down", h.intentsOf(c), nil)
	h.feed(ok(p, 40), fail(c, 60))
	same(t, "child intents after the restart", h.intentsOf(c), []string{"down 60 -"})
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(c, 90))
	same(t, "child intents after a second restart", h.intentsOf(c), nil)
}

// Within one batch the held DOWN is decided once: delivered, or, for a
// monitor that is flapping by then, suppressed by the flapping, whose end
// then announces it.
func TestHeldDownDecidedOnceInABatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	p := newMonitor(t, d, "router", nil)
	c, f := child(t, d, "web", p), child(t, d, "api", p)
	h.feed(fail(p, 1), fail(p, 2), fail(c, 3), fail(c, 4), fail(f, 3), fail(f, 4))
	if _, err := d.Writer.Exec(`UPDATE monitors SET flapping_since = ? WHERE id = ?`, store.FormatTime(at(4)), f); err != nil {
		t.Fatal(err)
	}
	h.stop()

	h = newHarness(t, d)
	for _, r := range []Result{ok(p, 40), fail(c, 60), fail(f, 60), fail(c, 90), fail(f, 90)} {
		h.p.Add(context.Background(), r)
	}
	h.run()
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 5 })
	same(t, "intents of the child", h.intentsOf(c), []string{"down 60 -"})
	same(t, "intents of the flapping child", h.intentsOf(f), []string{"down 60 flapping"})

	h.feed(fail(f, 603))
	same(t, "intents at the end of the flapping", h.intentsOf(f), []string{"down 60 flapping", "down 603 -"})
}
