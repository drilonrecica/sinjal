package results

import (
	"context"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// The flapping tests use a failure threshold of one, so that every failed
// check after a success is a transition, as is every success after it.
func flappy(t *testing.T, d *db.DB, name string) string {
	t.Helper()
	return newMonitor(t, d, name, func(m *store.HTTPMonitor) { m.FailureThreshold = 1 })
}

// flappingSince is the monitor's overlay in seconds after base, "-" when
// it is not flapping.
func (h *harness) flappingSince(id string) string {
	h.t.Helper()
	m := h.monitor(id)
	if m.FlappingSince == nil {
		return "-"
	}
	return store.FormatTime(*m.FlappingSince)
}

func flapAt(n int) string { return store.FormatTime(at(n)) }

// Scenario 4 of docs/20 at processor level: the fourth transition within
// ten minutes starts flapping, with one flapping intent; from then on
// incidents are recorded as before but their intents are suppressed.
func TestFlappingStartsAtTheFourthTransition(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := flappy(t, d, "web")

	h.feed(ok(id, 0), fail(id, 10), ok(id, 20), fail(id, 30))
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("flapping after three transitions, since %s", got)
	}
	same(t, "intents after three transitions", h.intentList(), []string{"down 10 -", "recovery 20 -", "down 30 -"})

	h.feed(ok(id, 40))
	m := h.monitor(id)
	if h.flappingSince(id) != flapAt(40) || m.State != "up" {
		t.Fatalf("after the fourth transition: state %s, flapping since %s", m.State, h.flappingSince(id))
	}
	same(t, "intents at the fourth transition", h.intentList()[3:], []string{"flapping 40 -", "recovery 40 flapping"})
	var second string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ? ORDER BY started_at DESC`, id).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if in := h.intent(3); in.IncidentID != second || in.MonitorID != id {
		t.Fatalf("flapping intent %+v, want the incident of the transition, %s", in, second)
	}

	// Checks and incidents go on; the real state is kept.
	h.feed(ok(id, 50), fail(id, 60))
	if m := h.monitor(id); m.State != "down" || h.flappingSince(id) != flapAt(40) {
		t.Fatalf("while flapping: state %s, flapping since %s", m.State, h.flappingSince(id))
	}
	h.feed(ok(id, 70))
	same(t, "intents while flapping", h.intentList()[5:], []string{"down 60 flapping", "recovery 70 flapping"})
	same(t, "incidents", incidents(t, d, id), []string{
		"10 20 http_status status 503, expected 200-399",
		"30 40 http_status status 503, expected 200-399",
		"60 70 http_status status 503, expected 200-399",
	})
	same(t, "events", events(t, d, id)[6:], []string{
		"notification_suppressed 40 recovery: flapping",
		"detected 60 status 503, expected 200-399", "declared_down 60 status 503, expected 200-399",
		"notification_suppressed 60 down: flapping",
		"recovered 70 down for 10s",
		"notification_suppressed 70 recovery: flapping",
	})
	if n := suppressions(t, d, id); n != 3 {
		t.Fatalf("%d suppression events, want 3", n)
	}
}

// Four transitions that do not fit into ten minutes are not flapping, and
// the edge is exact: a transition ten minutes old no longer counts.
func TestFlappingWindow(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	slow, edge, inside := flappy(t, d, "slow"), flappy(t, d, "edge"), flappy(t, d, "inside")
	h.feed(fail(slow, 0), ok(slow, 300), fail(slow, 600), ok(slow, 900), fail(slow, 1200), ok(slow, 1500))
	h.feed(fail(edge, 0), ok(edge, 100), fail(edge, 200), ok(edge, 600))
	h.feed(fail(inside, 0), ok(inside, 100), fail(inside, 200), ok(inside, 599))
	if got := h.flappingSince(slow); got != "-" {
		t.Errorf("a transition every five minutes is flapping since %s", got)
	}
	if got := h.flappingSince(edge); got != "-" {
		t.Errorf("the first transition was exactly ten minutes old, yet flapping since %s", got)
	}
	if got := h.flappingSince(inside); got != flapAt(599) {
		t.Errorf("four transitions in 9m59s: flapping since %s, want %s", got, flapAt(599))
	}
}

// With a failure threshold above one an incident starts at its first
// failure, and that is when its opening counts: the time a restart reads
// back from the incident.
func TestFlappingCountsAnOpeningAtItsStart(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	a, b := newMonitor(t, d, "a", nil), newMonitor(t, d, "b", nil) // thresholds 2 and 1
	// a: an outage from 0, confirmed at 5. At 604 its start is more than ten
	// minutes old, its confirmation is not: three transitions count.
	h.feed(fail(a, 0), fail(a, 5), ok(a, 100), fail(a, 200), fail(a, 205), ok(a, 604))
	if got := h.flappingSince(a); got != "-" {
		t.Errorf("a: flapping since %s; its first outage started over ten minutes before", got)
	}
	// b: the same, one transition sooner: four count.
	h.feed(fail(b, 0), fail(b, 5), ok(b, 100), fail(b, 200), fail(b, 205), ok(b, 599))
	if got := h.flappingSince(b); got != flapAt(599) {
		t.Errorf("b: flapping since %s, want %s", got, flapAt(599))
	}
}

// Failures that recover on the confirmation retry are no transitions.
func TestBlipsAreNotFlapping(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	for i := range 10 {
		h.feed(fail(id, 10*i), ok(id, 10*i+5))
	}
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("blips made the monitor flap since %s", got)
	}
	same(t, "intents", h.intentList(), nil)
}

// Flapping ends ten minutes after the last transition, at the next result:
// STABLE when the monitor is not down.
func TestFlappingEndsStable(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := flappy(t, d, "web")
	h.feed(fail(id, 0), ok(id, 10), fail(id, 20), ok(id, 30))
	before := len(h.intentList())

	h.feed(ok(id, 300), ok(id, 629))
	if got := h.flappingSince(id); got != flapAt(30) {
		t.Fatalf("flapping ended early: since %s", got)
	}
	h.feed(ok(id, 630))
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("still flapping since %s ten minutes after the last transition", got)
	}
	same(t, "intents at the end", h.intentList()[before:], []string{"stable 630 -"})
	if in := h.intent(before); in.IncidentID != "" || in.MonitorID != id {
		t.Fatalf("stable intent %+v", in)
	}
	// Once only, and the next outage is announced again.
	h.feed(ok(id, 660), fail(id, 700))
	same(t, "intents afterwards", h.intentList()[before:], []string{"stable 630 -", "down 700 -"})
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("one transition restarted flapping since %s", got)
	}
}

// When flapping ends while the monitor is down, the notification that was
// held back is due: DOWN, about the active incident.
func TestFlappingEndsDown(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := flappy(t, d, "web")
	h.feed(fail(id, 0), ok(id, 10), fail(id, 20), ok(id, 30), fail(id, 40))
	before := len(h.intentList())
	same(t, "the fifth transition", h.intentList()[before-1:], []string{"down 40 flapping"})

	h.feed(fail(id, 300), fail(id, 639))
	same(t, "intents while down and flapping", h.intentList()[before:], nil)
	h.feed(fail(id, 640), fail(id, 670))
	if m := h.monitor(id); m.State != "down" || m.FlappingSince != nil {
		t.Fatalf("after the end: state %s, flapping %v", m.State, m.FlappingSince)
	}
	same(t, "intents at the end", h.intentList()[before:], []string{"down 640 -"})
	var active string
	if err := d.Reader.QueryRow(`SELECT id FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if in := h.intent(before); in.IncidentID != active {
		t.Fatalf("down intent for incident %q, want the active one %q", in.IncidentID, active)
	}
	same(t, "incidents", incidents(t, d, id)[2:], []string{"40 - http_status status 503, expected 200-399"})
}

// The result that reveals the end of flapping may itself be a transition.
// The end is settled first, for the state before it; the transition is
// then an ordinary one, announced and counted afresh.
func TestFlappingEndsAtATransition(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	up, down := flappy(t, d, "up"), flappy(t, d, "down")
	h.feed(fail(up, 0), ok(up, 10), fail(up, 20), ok(up, 30))
	h.feed(fail(down, 0), ok(down, 10), fail(down, 20), ok(down, 30), fail(down, 40))
	before := len(h.intentList())

	h.feed(fail(up, 700))
	same(t, "up, then failing", h.intentList()[before:], []string{"stable 700 -", "down 700 -"})
	h.feed(ok(down, 700))
	same(t, "down, then recovering", h.intentList()[before+2:], []string{"down 700 -", "recovery 700 -"})
	for _, id := range []string{up, down} {
		if got := h.flappingSince(id); got != "-" {
			t.Errorf("flapping since %s after it ended", got)
		}
	}
	if n := suppressions(t, d, up) + suppressions(t, d, down); n != 3 {
		t.Fatalf("%d suppression events, want the 3 from before the end", n)
	}
}

// All of it inside one batch: each result sees what the earlier ones wrote.
func TestFlappingWithinOneBatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.flushAfter, h.p.batchMax = time.Hour, 7
	h.run()
	id := flappy(t, d, "web")
	for _, r := range []Result{fail(id, 0), ok(id, 10), fail(id, 20), ok(id, 30), fail(id, 40), fail(id, 639), fail(id, 640)} {
		h.p.Add(context.Background(), r)
	}
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 7 })
	same(t, "intents", h.intentList(), []string{
		"down 0 -", "recovery 10 -", "down 20 -", "flapping 30 -", "recovery 30 flapping", "down 40 flapping", "down 640 -",
	})
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("flapping since %s", got)
	}
}

// The end is timed from the incident's start as well, also for an opening
// in the same batch, which is the only time it is not read from the row.
func TestFlappingEndIsTimedFromTheStartOfAnOutage(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.flushAfter, h.p.batchMax = time.Hour, 9
	h.run()
	id := newMonitor(t, d, "web", nil) // thresholds 2 and 1
	for _, r := range []Result{
		fail(id, 0), fail(id, 1), ok(id, 10), fail(id, 20), fail(id, 21), ok(id, 30), // flapping at 30
		fail(id, 40), fail(id, 45), // down from 40, confirmed at 45
		fail(id, 642), // ten minutes after 40, not yet after 45
	} {
		h.p.Add(context.Background(), r)
	}
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 9 })
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("flapping since %s, ten minutes after the outage began", got)
	}
	same(t, "the last intents", h.intentList()[5:], []string{"down 45 flapping", "down 642 -"})
}

// Nothing about flapping is kept in memory: a new process counts the
// transitions before it and ends a flapping it did not start.
func TestFlappingAcrossRestarts(t *testing.T) {
	d, _ := testDB(t)
	id := flappy(t, d, "web")
	h := newHarness(t, d).run()
	h.feed(fail(id, 0), ok(id, 10), fail(id, 20))
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(id, 25), ok(id, 30))
	if got := h.flappingSince(id); got != flapAt(30) {
		t.Fatalf("after a restart mid-window: flapping since %s, want %s", got, flapAt(30))
	}
	same(t, "intents after the first restart", h.intentList(), []string{"flapping 30 -", "recovery 30 flapping"})
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(id, 40))
	same(t, "intents while flapping, after the second restart", h.intentList(), []string{"down 40 flapping"})
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(id, 639))
	if got := h.flappingSince(id); got != flapAt(30) {
		t.Fatalf("flapping ended early after a restart: since %s", got)
	}
	h.feed(fail(id, 640))
	same(t, "intents at the end, after the third restart", h.intentList(), []string{"down 640 -"})
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("still flapping since %s", got)
	}
}

// A pause clears the overlay, and the incident it closes is no recovery:
// it does not count as a transition.
func TestFlappingAndPause(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	ctx := context.Background()
	pause := func(id string, from, to int) {
		t.Helper()
		if _, err := store.PauseMonitor(ctx, d, id, at(from)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResumeMonitor(ctx, d, id, at(to)); err != nil {
			t.Fatal(err)
		}
	}

	// Three outages, two of them ended by a pause: three transitions.
	id := flappy(t, d, "paused")
	h.feed(fail(id, 0))
	pause(id, 5, 6)
	h.feed(fail(id, 10))
	pause(id, 15, 16)
	h.feed(fail(id, 20))
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("pauses counted as recoveries: flapping since %s", got)
	}
	same(t, "incidents", incidents(t, d, id), []string{
		"0 5 http_status status 503, expected 200-399",
		"10 15 http_status status 503, expected 200-399",
		"20 - http_status status 503, expected 200-399",
	})
	// The fourth real transition does start it.
	h.feed(ok(id, 30))
	if got := h.flappingSince(id); got != flapAt(30) {
		t.Fatalf("flapping since %s, want %s", got, flapAt(30))
	}

	// Pausing a flapping monitor clears the overlay without any intent.
	before := len(h.intentList())
	pause(id, 40, 50)
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("flapping since %s after a pause", got)
	}
	h.feed(ok(id, 60))
	same(t, "intents after the pause", h.intentList()[before:], nil)
}

// An overlay without any transition behind it (set by hand) ends at the
// next result instead of staying for good.
func TestFlappingWithoutTransitionsEnds(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := flappy(t, d, "web")
	if _, err := d.Writer.Exec(`UPDATE monitors SET flapping_since = ? WHERE id = ?`, store.FormatTime(base), id); err != nil {
		t.Fatal(err)
	}
	h.feed(ok(id, 1))
	if got := h.flappingSince(id); got != "-" {
		t.Fatalf("flapping since %s", got)
	}
	same(t, "intents", h.intentList(), []string{"stable 1 -"})
}
