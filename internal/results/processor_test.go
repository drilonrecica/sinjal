package results

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/store"
)

var base = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// at is base plus n seconds: every result in a test gets its own second.
func at(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

func testDB(t testing.TB) (*db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sinjal.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	return d, path
}

// newMonitor creates an enabled HTTP monitor (pending, thresholds 2 and 1,
// retry delay 20 ms) after applying edit.
func newMonitor(t testing.TB, d *db.DB, name string, edit func(*store.MonitorInput)) string {
	t.Helper()
	in := store.MonitorInput{Name: name, Enabled: true, RetryDelayMS: 20,
		HTTP: store.HTTPConfig{URL: "https://example.com/" + name, FollowRedirects: true, TLSExpiryEnabled: true}}
	if edit != nil {
		edit(&in)
	}
	id, err := store.CreateMonitor(context.Background(), d, in, base)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type retryCall struct {
	id    string
	delay time.Duration
}

// harness runs a processor with a short flush time and records its calls.
type harness struct {
	t      *testing.T
	d      *db.DB
	p      *Processor
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	retries  []retryCall
	notified []string
	intents  []incident.Intent
	changes  []Change
}

func newHarness(t *testing.T, d *db.DB) *harness {
	t.Helper()
	h := &harness{t: t, d: d, done: make(chan struct{})}
	h.p = New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), time.UTC,
		func(id string, delay time.Duration) {
			h.mu.Lock()
			h.retries = append(h.retries, retryCall{id, delay})
			h.mu.Unlock()
		},
		func(id string) {
			h.mu.Lock()
			h.notified = append(h.notified, id)
			h.mu.Unlock()
		},
		func(in incident.Intent) {
			h.mu.Lock()
			h.intents = append(h.intents, in)
			h.mu.Unlock()
		},
		func(c Change) {
			h.mu.Lock()
			h.changes = append(h.changes, c)
			h.mu.Unlock()
		})
	h.p.flushAfter = 2 * time.Millisecond
	h.p.retryAfter = 20 * time.Millisecond
	return h
}

func (h *harness) run() *harness {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.p.Run(ctx); close(h.done) }()
	h.t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	h.cancel()
	<-h.done
}

// wait polls until cond holds.
func (h *harness) wait(what string, cond func(Stats) bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond(h.p.Stats()) {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; stats %+v", what, h.p.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

// feed adds results one batch at a time and waits for each to be handled.
func (h *harness) feed(results ...Result) {
	h.t.Helper()
	for _, r := range results {
		before := h.p.Stats()
		if !h.p.Add(context.Background(), r) {
			h.t.Fatal("Add returned false")
		}
		h.wait("the result to be processed", func(s Stats) bool {
			return s.Persisted+s.Discarded == before.Persisted+before.Discarded+1
		})
	}
}

func (h *harness) monitor(id string) store.Monitor {
	h.t.Helper()
	m, err := store.GetMonitor(context.Background(), h.d.Reader, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

// calls returns copies of the recorded retry and notify calls.
func (h *harness) calls() (retries []retryCall, notified []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]retryCall(nil), h.retries...), append([]string(nil), h.notified...)
}

func (h *harness) retryCount() int {
	retries, _ := h.calls()
	return len(retries)
}

func (h *harness) rows(id string) int {
	h.t.Helper()
	var n int
	if err := h.d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func ok(id string, n int) Result {
	return Result{MonitorID: id, CheckedAt: at(n), Duration: 12 * time.Millisecond, Success: true, Status: "200"}
}

func fail(id string, n int) Result {
	return Result{MonitorID: id, CheckedAt: at(n), Duration: 30 * time.Millisecond, Status: "503",
		Kind: "http_status", Message: "status 503, expected 200-399", Snippet: "<h1>down</h1>"}
}

func timeIs(got *time.Time, want time.Time) bool { return got != nil && got.Equal(want) }

func TestStoresResultRows(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	f := fail(id, 1)
	f.Duration = 1500 * time.Microsecond
	f.Metadata = `{"tls":{"days_remaining":3}}`
	h.feed(f, Result{MonitorID: id, CheckedAt: at(2), Success: true})

	rows, err := d.Reader.Query(`SELECT checked_at, duration_ms, success, protocol_status, error_kind,
		error_message, diagnostic_snippet, metadata_json FROM check_results WHERE monitor_id = ? ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var checked string
		var ms float64
		var success int
		var status, kind, msg, snippet, meta sql.NullString
		if err := rows.Scan(&checked, &ms, &success, &status, &kind, &msg, &snippet, &meta); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %g %d %v %v %v %v %v", checked, ms, success, status, kind, msg, snippet, meta))
	}
	want := []string{
		`2026-10-06T12:00:01Z 1.5 0 {503 true} {http_status true} {status 503, expected 200-399 true} {<h1>down</h1> true} {{"tls":{"days_remaining":3}} true}`,
		`2026-10-06T12:00:02Z 0 1 { false} { false} { false} { false} { false}`,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Scenario 1 of docs/20: failure -> retry -> DOWN.
func TestFailureRetryDown(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)

	h.feed(ok(id, 1))
	m := h.monitor(id)
	if m.State != "up" || !m.StateSince.Equal(at(1)) || !timeIs(m.LastCheckAt, at(1)) || !timeIs(m.LastSuccessAt, at(1)) || m.LastFailureAt != nil {
		t.Fatalf("after the first success: %+v", m)
	}

	h.feed(fail(id, 2))
	m = h.monitor(id)
	if m.State != "pending" || !m.StateSince.Equal(at(2)) || !timeIs(m.LastFailureAt, at(2)) || !timeIs(m.LastSuccessAt, at(1)) {
		t.Fatalf("after the first failure: %+v", m)
	}
	if retries, _ := h.calls(); len(retries) != 1 || retries[0] != (retryCall{id, 20 * time.Millisecond}) {
		t.Fatalf("retries after the first failure: %+v", retries)
	}

	h.feed(fail(id, 3))
	m = h.monitor(id)
	if m.State != "down" || !m.StateSince.Equal(at(3)) || !timeIs(m.LastCheckAt, at(3)) {
		t.Fatalf("after the confirmation failure: %+v", m)
	}
	h.feed(fail(id, 4), fail(id, 5))
	m = h.monitor(id)
	if m.State != "down" || !m.StateSince.Equal(at(3)) || !timeIs(m.LastFailureAt, at(5)) {
		t.Fatalf("still down, since the confirmation: %+v", m)
	}
	retries, notified := h.calls()
	if len(retries) != 1 {
		t.Fatalf("no retries once down, got %+v", retries)
	}
	if h.rows(id) != 5 || len(notified) != 5 {
		t.Fatalf("%d rows, %d notifications", h.rows(id), len(notified))
	}
}

// Scenario 2: the retry succeeds, the monitor never goes down.
func TestRetrySuccessIsNoOutage(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(ok(id, 1), fail(id, 2), ok(id, 3))
	if m := h.monitor(id); m.State != "up" || !m.StateSince.Equal(at(3)) {
		t.Fatalf("monitor: %+v", m)
	}
	// The earlier failure is forgotten: the next one starts a new count.
	h.feed(fail(id, 4))
	if m := h.monitor(id); m.State != "pending" || h.retryCount() != 2 {
		t.Fatalf("monitor: %+v, retries %d", m, h.retryCount())
	}
}

// Scenario 3: DOWN -> success -> recovered.
func TestRecovery(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(fail(id, 1), fail(id, 2))
	if m := h.monitor(id); m.State != "down" {
		t.Fatalf("a new monitor that fails twice is down: %+v", m)
	}
	h.feed(ok(id, 3))
	m := h.monitor(id)
	if m.State != "up" || !m.StateSince.Equal(at(3)) || !timeIs(m.LastSuccessAt, at(3)) || !timeIs(m.LastFailureAt, at(2)) {
		t.Fatalf("after recovery: %+v", m)
	}
}

func TestThresholdsFromTheMonitorRow(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", func(m *store.MonitorInput) {
		m.FailureThreshold, m.SuccessThreshold, m.RetryDelayMS = 3, 3, 1500
	})
	states := func(results ...Result) string {
		out := ""
		for _, r := range results {
			h.feed(r)
			out += h.monitor(id).State + " "
		}
		return out
	}
	if got := states(fail(id, 1), fail(id, 2), fail(id, 3)); got != "pending pending down " {
		t.Fatalf("going down: %s", got)
	}
	if retries, _ := h.calls(); len(retries) != 2 || retries[1].delay != 1500*time.Millisecond {
		t.Fatalf("retries: %+v", retries)
	}
	if got := states(ok(id, 4), ok(id, 5), fail(id, 6), ok(id, 7), ok(id, 8), ok(id, 9)); got != "down down down down down up " {
		t.Fatalf("recovering: %s", got)
	}
	// An edit applies to the next result.
	if _, err := d.Writer.Exec(`UPDATE monitors SET failure_threshold = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if got := states(fail(id, 10)); got != "down " {
		t.Fatalf("after lowering the threshold: %s", got)
	}
}

// Results of one monitor that land in the same batch are applied in order,
// each seeing the previous one's outcome.
func TestSeveralResultsInOneBatch(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.flushAfter, h.p.batchMax = time.Hour, 4
	h.run()
	a, b := newMonitor(t, d, "a", nil), newMonitor(t, d, "b", nil)
	for _, r := range []Result{fail(a, 1), ok(b, 1), fail(a, 2)} {
		h.p.Add(context.Background(), r)
	}
	time.Sleep(50 * time.Millisecond)
	if s := h.p.Stats(); s.Persisted != 0 {
		t.Fatalf("flushed below the batch size and before the time: %+v", s)
	}
	h.p.Add(context.Background(), ok(a, 3))
	h.wait("the batch", func(s Stats) bool { return s.Persisted == 4 })
	ma, mb := h.monitor(a), h.monitor(b)
	if ma.State != "up" || !ma.StateSince.Equal(at(3)) || !timeIs(ma.LastFailureAt, at(2)) || mb.State != "up" {
		t.Fatalf("a: %+v\nb: %+v", ma, mb)
	}
	// a went pending, down, up inside the batch: its last result asks for
	// no retry, so none is requested, and each monitor is announced once.
	if retries, notified := h.calls(); len(retries) != 0 || len(notified) != 2 {
		t.Fatalf("retries %+v, notified %v", retries, notified)
	}
}

func TestFlushesAfterTheWaitTime(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.flushAfter = 30 * time.Millisecond
	h.run()
	id := newMonitor(t, d, "web", nil)
	begin := time.Now()
	h.p.Add(context.Background(), ok(id, 1))
	h.wait("the timed flush", func(s Stats) bool { return s.Persisted == 1 })
	if waited := time.Since(begin); waited < 30*time.Millisecond {
		t.Fatalf("a single result was written after %v, before the wait time", waited)
	}
}

func TestLateResultsOfPausedAndDeletedMonitors(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	paused := newMonitor(t, d, "paused", func(m *store.MonitorInput) { m.Enabled = false })
	live := newMonitor(t, d, "live", nil)
	gone := newMonitor(t, d, "gone", nil)
	if err := store.DeleteMonitor(context.Background(), d, gone); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(paused, 1), fail(gone, 1), ok(live, 1), fail(paused, 2))
	if s := h.p.Stats(); s.Persisted != 1 || s.Discarded != 3 || s.Warning != "" {
		t.Fatalf("stats: %+v", s)
	}
	m := h.monitor(paused)
	if m.State != "paused" || !m.StateSince.Equal(base) || m.LastCheckAt != nil || h.rows(paused) != 0 {
		t.Fatalf("a paused monitor must not change: %+v", m)
	}
	if retries, notified := h.calls(); len(notified) != 1 || notified[0] != live || len(retries) != 0 {
		t.Fatalf("notified %v, retries %+v", notified, retries)
	}
}

// Counters belong to one stretch of a state. A pause and resume in between
// (same state name, new "since") starts the count again.
func TestCountersResetWhenTheStateWasChangedElsewhere(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(fail(id, 1))
	if m := h.monitor(id); m.State != "pending" {
		t.Fatalf("monitor: %+v", m)
	}
	// Paused and resumed by the UI: pending again, since the resume.
	if _, err := d.Writer.Exec(`UPDATE monitors SET current_state = 'pending', current_state_since = ? WHERE id = ?`,
		store.FormatTime(at(10)), id); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(id, 11))
	if m := h.monitor(id); m.State != "pending" || !m.StateSince.Equal(at(10)) {
		t.Fatalf("the failure before the pause must not count: %+v", m)
	}
	h.feed(fail(id, 12))
	if m := h.monitor(id); m.State != "down" {
		t.Fatalf("two failures after the resume: %+v", m)
	}
	if n := h.retryCount(); n != 2 {
		t.Fatalf("%d retries, want 2", n)
	}
}

func TestCountersAreKeptOnlyWhileNeeded(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	a := newMonitor(t, d, "a", nil)
	b := newMonitor(t, d, "b", func(m *store.MonitorInput) { m.SuccessThreshold = 3 })
	c := newMonitor(t, d, "c", nil)
	h.feed(fail(a, 1), fail(b, 1), fail(b, 2), ok(b, 3), fail(c, 1), ok(c, 2))
	if err := store.DeleteMonitor(context.Background(), d, a); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(a, 2)) // a late result for the deleted monitor
	h.stop()
	// a: deleted. b: down with one of three successes. c: back up.
	if len(h.p.counters) != 1 || h.p.counters[b].counters.Successes != 1 {
		t.Fatalf("counters: %+v", h.p.counters)
	}
}

func TestTLSNotAfter(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	expiry := time.Date(2027, 1, 15, 8, 30, 0, 0, time.UTC)
	r := ok(id, 1)
	r.TLSNotAfter = &expiry
	h.feed(r)
	if m := h.monitor(id); !timeIs(m.TLSNotAfter, expiry) {
		t.Fatalf("expiry not stored: %+v", m.TLSNotAfter)
	}
	// A failure without a certificate (connection refused) says nothing.
	h.feed(fail(id, 2))
	if m := h.monitor(id); !timeIs(m.TLSNotAfter, expiry) {
		t.Fatalf("expiry lost on a failed check: %+v", m.TLSNotAfter)
	}
	// A failure that did see a certificate updates it.
	renewed := expiry.AddDate(0, 3, 0)
	r = fail(id, 3)
	r.TLSNotAfter = &renewed
	h.feed(r)
	if m := h.monitor(id); !timeIs(m.TLSNotAfter, renewed) {
		t.Fatalf("renewed expiry not stored: %+v", m.TLSNotAfter)
	}
	// A success without one: the monitor is not HTTPS any more.
	h.feed(ok(id, 4))
	if m := h.monitor(id); m.TLSNotAfter != nil {
		t.Fatalf("stale expiry kept: %+v", m.TLSNotAfter)
	}
}

// SQLITE_BUSY through every retry: the batch is held, a warning is raised,
// and the results are stored once the database is free again.
func TestBusyExhaustionHoldsTheBatch(t *testing.T) {
	d, path := testDB(t)
	// Fail fast instead of waiting 5 s per attempt.
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
	s := h.p.Stats()
	if s.Persisted != 0 || !strings.Contains(s.Warning, "busy") {
		t.Fatalf("stats while the database is locked: %+v", s)
	}
	if h.rows(id) != 0 || h.retryCount() != 0 || h.monitor(id).State != "pending" {
		t.Fatal("nothing may be applied before the write succeeds")
	}

	if _, err := lock.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	h.wait("the held batch to be stored", func(s Stats) bool { return s.Persisted == 2 })
	if s := h.p.Stats(); s.Warning != "" || s.Discarded != 0 {
		t.Fatalf("stats after recovery: %+v", s)
	}
	// Both failures were counted exactly once, however often the
	// transaction was attempted.
	if m := h.monitor(id); m.State != "down" || h.rows(id) != 2 {
		t.Fatalf("monitor after recovery: %+v, %d rows", m, h.rows(id))
	}
}

// Any other write error is treated the same way, and while a full batch is
// held the processor stops taking results: Add blocks instead of the
// backlog growing.
func TestWriteErrorHoldsTheBatchAndPushesBack(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d)
	h.p.batchMax = 8
	h.run()
	id := newMonitor(t, d, "web", nil)
	if _, err := d.Writer.Exec(`ALTER TABLE check_results RENAME TO check_results_gone`); err != nil {
		t.Fatal(err)
	}

	accepted := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		added := h.p.Add(ctx, ok(id, accepted+1))
		cancel()
		if !added {
			break
		}
		if accepted++; accepted > 4*queueSize {
			t.Fatal("Add never blocked: the backlog is unbounded")
		}
	}
	// One full batch held plus a full queue, nothing more.
	if s := h.p.Stats(); accepted != h.p.batchMax+queueSize || s.Queued != queueSize || s.Persisted != 0 || s.Warning == "" || s.FailedFlushes == 0 {
		t.Fatalf("accepted %d, stats %+v", accepted, s)
	}

	if _, err := d.Writer.Exec(`ALTER TABLE check_results_gone RENAME TO check_results`); err != nil {
		t.Fatal(err)
	}
	h.wait("the backlog to be stored", func(s Stats) bool { return s.Persisted == uint64(accepted) })
	if s := h.p.Stats(); s.Warning != "" || s.Queued != 0 {
		t.Fatalf("stats after recovery: %+v", s)
	}
	if h.rows(id) != accepted {
		t.Fatalf("%d rows for %d accepted results", h.rows(id), accepted)
	}
}

func TestShutdownStoresWhatIsQueued(t *testing.T) {
	d, _ := testDB(t)
	// No retry, notify or intent hooks: all are optional.
	p := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), time.UTC, nil, nil, nil, nil)
	p.flushAfter = time.Hour
	id := newMonitor(t, d, "web", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	for i := range 200 {
		if !p.Add(context.Background(), Result{MonitorID: id, CheckedAt: at(i), Success: i%2 == 0}) {
			t.Fatal("Add returned false")
		}
	}
	cancel()
	<-done
	var n int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 200 || p.Stats().Persisted != 200 || p.Stats().Queued != 0 {
		t.Fatalf("%d rows, stats %+v", n, p.Stats())
	}
	// After it has stopped, Add gives up with its context instead of hanging
	// once the queue is full.
	short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	for i := 0; i <= queueSize; i++ {
		if !p.Add(short, ok(id, 1)) {
			return
		}
	}
	t.Fatal("Add kept accepting results with nobody reading")
}

// Many workers adding at once (meaningful under -race): every result is
// stored exactly once.
func TestConcurrentAdd(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	var ids []string
	for i := range 8 {
		ids = append(ids, newMonitor(t, d, fmt.Sprint("m", i), nil))
	}
	var wg sync.WaitGroup
	for g, id := range ids {
		wg.Go(func() {
			for i := range 250 {
				h.p.Add(context.Background(), Result{MonitorID: id, CheckedAt: at(i), Success: (i+g)%3 != 0})
				h.p.Stats()
			}
		})
	}
	wg.Wait()
	h.wait("all results", func(s Stats) bool { return s.Persisted == 2000 })
	for _, id := range ids {
		if h.rows(id) != 250 {
			t.Fatalf("monitor %s has %d rows", id, h.rows(id))
		}
	}
}
