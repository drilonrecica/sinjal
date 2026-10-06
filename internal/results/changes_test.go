package results

import "testing"

// changesOf lists the incident changes announced for a monitor as
// "kind", in order.
func (h *harness) changesOf(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.changes {
		if c.MonitorID == id {
			out = append(out, c.Kind)
		}
	}
	return out
}

// An outage announces its opening once it is committed, a recovery its
// closing, and staying down announces nothing.
func TestIncidentChangesAreAnnounced(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)

	h.feed(fail(id, 1))
	same(t, "after one failure", h.changesOf(id), nil)
	h.feed(fail(id, 2), fail(id, 30))
	same(t, "after the outage", h.changesOf(id), []string{ChangeOpened})
	h.feed(ok(id, 60), ok(id, 90))
	same(t, "after the recovery", h.changesOf(id), []string{ChangeOpened, ChangeClosed})

	var want string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ?`, id).Scan(&want); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.changes {
		if c.IncidentID != want || c.MonitorID != id {
			t.Fatalf("change %+v, want incident %s of monitor %s", c, want, id)
		}
	}
}

// A timeline entry on a running incident (a held DOWN, its resumption) is
// an update; a held DOWN that is caught up later announces it as such.
func TestTimelineEntriesAreAnnouncedAsUpdates(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	p := newMonitor(t, d, "router", nil)
	c := child(t, d, "web", p)

	h.feed(fail(p, 1), fail(p, 2), fail(c, 3), fail(c, 4))
	same(t, "child while held", h.changesOf(c), []string{ChangeOpened, ChangeUpdated})
	h.feed(ok(p, 40), fail(c, 60))
	same(t, "child once resumed", h.changesOf(c), []string{ChangeOpened, ChangeUpdated, ChangeUpdated})
	h.feed(ok(c, 120), ok(c, 150))
	same(t, "child once recovered", h.changesOf(c), []string{ChangeOpened, ChangeUpdated, ChangeUpdated, ChangeClosed})
}
