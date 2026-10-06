package engine

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

var created = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// env is a migrated database file and its key; engines are started on it
// one after the other, like restarts of the process.
type env struct {
	t   *testing.T
	d   *db.DB
	key *vault.Key

	mu        sync.Mutex
	updated   []string                                  // monitor ids announced by the engines, in order
	incidents func(event, incidentID, monitorID string) // given to engines that start
}

// announced is how often a monitor has been announced as updated.
func (e *env) announced(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, got := range e.updated {
		if got == id {
			n++
		}
	}
	return n
}

func (e *env) announce(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.updated = append(e.updated, id)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadOrCreate(context.Background(), dir, d.Reader, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, d: d, key: key}
}

// monitor creates an enabled HTTP monitor for url: thresholds 2 and 1, a
// 20 ms retry delay and the default 30 s interval, after applying edit.
func (e *env) monitor(name, url string, edit func(*store.HTTPMonitor)) string {
	e.t.Helper()
	in := store.HTTPMonitor{Name: name, Enabled: true, RetryDelayMS: 20, TimeoutMS: 5000,
		Config: store.HTTPConfig{URL: url, FollowRedirects: true, TLSExpiryEnabled: true}}
	if edit != nil {
		edit(&in)
	}
	id, err := store.CreateHTTPMonitor(context.Background(), e.d, in, created)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Writer.Exec(query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) get(id string) store.Monitor {
	e.t.Helper()
	m, err := store.GetMonitor(context.Background(), e.d.Reader, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) rows(id string) int {
	e.t.Helper()
	var n int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// running is a started engine.
type running struct {
	*Engine
	t       *testing.T
	cancel  context.CancelFunc
	stopped bool
}

// start runs an engine with four workers on the environment until stop or
// the end of the test.
func (e *env) start() *running {
	e.t.Helper()
	return e.startWith(4)
}

func (e *env) startWith(workers int) *running {
	e.t.Helper()
	eng := New(e.d, e.key, workers, "Sinjal/test", time.UTC, e.announce, e.incidents, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.Start(ctx); err != nil {
		cancel()
		e.t.Fatal(err)
	}
	r := &running{Engine: eng, t: e.t, cancel: cancel}
	e.t.Cleanup(r.stop)
	return r
}

// stop shuts the engine down and fails the test if that takes long.
func (r *running) stop() {
	if r.stopped {
		return
	}
	r.stopped = true
	r.cancel()
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		r.t.Error("the engine did not stop within 5 s")
	}
}

// eventually polls until cond holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// target is an HTTP server that answers 200 or 503 and counts requests.
type target struct {
	*httptest.Server
	hits atomic.Int64
	fail atomic.Bool
}

func newTarget(t *testing.T) *target {
	t.Helper()
	tg := &target{}
	tg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.hits.Add(1)
		if tg.fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(tg.Close)
	return tg
}

// blockingTarget holds every request until release is called (or the
// client gives up) and reports each arrival on entered.
func blockingTarget(t *testing.T) (url string, entered chan struct{}, release func()) {
	t.Helper()
	entered = make(chan struct{}, 16)
	held := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-held:
		case <-r.Context().Done():
		}
	}))
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	// Registered before the engines of the test, so it runs after they
	// have stopped: nothing is left waiting when the server closes.
	t.Cleanup(func() { release(); srv.Close() })
	return srv.URL, entered, release
}

func TestStartChecksEnabledMonitors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, b, off := newTarget(t), newTarget(t), newTarget(t)
	ida, idb := e.monitor("a", a.URL, nil), e.monitor("b", b.URL, nil)
	idOff := e.monitor("off", off.URL, func(m *store.HTTPMonitor) { m.Enabled = false })

	r := e.start()
	eventually(t, "both monitors to be up", func() bool {
		return e.get(ida).State == "up" && e.get(idb).State == "up"
	})
	if r.sch.Len() != 2 {
		t.Fatalf("%d monitors scheduled, want 2", r.sch.Len())
	}
	r.stop()

	for _, id := range []string{ida, idb} {
		m := e.get(id)
		if m.LastCheckAt == nil || m.LastSuccessAt == nil || e.rows(id) != 1 {
			t.Fatalf("%s: %+v, %d rows", m.Name, m, e.rows(id))
		}
	}
	if m := e.get(idOff); m.State != "paused" || m.LastCheckAt != nil || off.hits.Load() != 0 {
		t.Fatalf("the disabled monitor was checked: %+v, %d requests", m, off.hits.Load())
	}
}

// Scenario "restart during an outage" at state level: the monitor stays
// DOWN since the original moment, is checked again promptly, and recovers.
func TestRestartKeepsState(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	tg.fail.Store(true)
	id := e.monitor("api", tg.URL, nil)

	r := e.start()
	eventually(t, "the monitor to be down", func() bool { return e.get(id).State == "down" })
	r.stop()
	down := e.get(id)
	if e.rows(id) != 2 {
		t.Fatalf("%d results before the restart, want the check and its retry", e.rows(id))
	}

	// Restart while the target is still failing.
	r = e.start()
	eventually(t, "a check after the restart", func() bool { return e.rows(id) > 2 })
	r.stop()
	if m := e.get(id); m.State != "down" || !m.StateSince.Equal(down.StateSince) {
		t.Fatalf("after the restart: state %s since %v, want down since %v", m.State, m.StateSince, down.StateSince)
	}

	// Restart after the target has recovered.
	tg.fail.Store(false)
	r = e.start()
	eventually(t, "the monitor to recover", func() bool { return e.get(id).State == "up" })
	r.stop()
	if m := e.get(id); !m.StateSince.After(down.StateSince) && !m.StateSince.Equal(down.StateSince) {
		t.Fatalf("up since %v, before the outage began at %v", m.StateSince, down.StateSince)
	}
}

func TestPauseAndResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("api", tg.URL, nil)
	// One second is the scheduler's shortest interval; validation would
	// not allow it.
	e.exec(`UPDATE monitors SET interval_seconds = 1 WHERE id = ?`, id)

	r := e.start()
	eventually(t, "the monitor to be up", func() bool { return e.get(id).State == "up" })

	if _, err := r.Pause(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if m := e.get(id); m.State != "paused" || m.Enabled {
		t.Fatalf("after pause: %+v", m)
	}
	eventually(t, "the monitor to leave the schedule", func() bool { return r.sch.Len() == 0 })
	var open int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM monitor_pauses WHERE monitor_id = ? AND resumed_at IS NULL`, id).Scan(&open); err != nil || open != 1 {
		t.Fatalf("open pause intervals = %d, %v", open, err)
	}
	// A check that was already on its way may still arrive; after that the
	// target must see nothing for longer than the interval.
	time.Sleep(100 * time.Millisecond)
	hits := tg.hits.Load()
	time.Sleep(1300 * time.Millisecond)
	if got := tg.hits.Load(); got != hits {
		t.Fatalf("%d checks ran while paused", got-hits)
	}
	if m := e.get(id); m.State != "paused" {
		t.Fatalf("state while paused: %s", m.State)
	}

	if _, err := r.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if m := e.get(id); m.State != "pending" && m.State != "up" || !m.Enabled {
		t.Fatalf("after resume: %+v", m)
	}
	// Promptly: well inside the one-second interval.
	begin := time.Now()
	eventually(t, "the monitor to be up again", func() bool { return e.get(id).State == "up" })
	if took := time.Since(begin); took > 900*time.Millisecond {
		t.Fatalf("the first check after resume took %v", took)
	}
	if r.sch.Len() != 1 || tg.hits.Load() <= hits {
		t.Fatalf("scheduled %d, requests %d (before resume %d)", r.sch.Len(), tg.hits.Load(), hits)
	}
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM monitor_pauses WHERE monitor_id = ? AND resumed_at IS NULL`, id).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open pause intervals after resume = %d, %v", open, err)
	}
}

func TestPauseAndResumeAreIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("api", tg.URL, nil)
	r := e.start()
	eventually(t, "the monitor to be up", func() bool { return e.get(id).State == "up" })
	since := e.get(id).StateSince

	// Resuming a monitor that runs must neither reset its state nor check
	// it again.
	if changed, err := r.Resume(context.Background(), id); err != nil || changed {
		t.Fatalf("needless resume: changed %v, %v", changed, err)
	}
	time.Sleep(150 * time.Millisecond)
	if m := e.get(id); m.State != "up" || !m.StateSince.Equal(since) || tg.hits.Load() != 1 {
		t.Fatalf("after a needless resume: %+v, %d requests", m, tg.hits.Load())
	}

	for i := range 2 {
		if changed, err := r.Pause(context.Background(), id); err != nil || changed != (i == 0) {
			t.Fatalf("pause %d: changed %v, %v", i+1, changed, err)
		}
	}
	if n := e.rowsIn("monitor_pauses", id); n != 1 {
		t.Fatalf("%d pause intervals after pausing twice", n)
	}

	if _, err := r.Pause(context.Background(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Pause of an unknown monitor: %v", err)
	}
	if _, err := r.Resume(context.Background(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resume of an unknown monitor: %v", err)
	}
}

func (e *env) rowsIn(table, id string) int {
	e.t.Helper()
	var n int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE monitor_id = ?`, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A check that was running when its monitor was paused says nothing any
// more: its result is discarded.
func TestPauseDuringACheckDiscardsItsResult(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	url, entered, release := blockingTarget(t)
	id := e.monitor("slow", url, nil)

	r := e.start()
	<-entered
	if _, err := r.Pause(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	release()
	eventually(t, "the late result to be discarded", func() bool { return r.proc.Stats().Discarded == 1 })
	if m := e.get(id); m.State != "paused" || m.LastCheckAt != nil || e.rows(id) != 0 {
		t.Fatalf("after the late result: %+v, %d rows", m, e.rows(id))
	}
}

// A job that was already waiting for a worker when its monitor was paused
// does not reach the target.
func TestPausedMonitorIsNotCheckedByAWaitingJob(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	url, entered, release := blockingTarget(t)
	tg := newTarget(t)
	// First checks go out in id order and ids are random: the monitor
	// with the lower id gets the blocking target and the only worker.
	slow, waiting := e.monitor("one", url, nil), e.monitor("two", url, nil)
	if slow > waiting {
		slow, waiting = waiting, slow
	}
	e.exec(`UPDATE http_monitor_config SET url = ? WHERE monitor_id = ?`, tg.URL, waiting)

	r := e.startWith(1)
	<-entered
	eventually(t, "the second job to wait for the worker", func() bool { return r.pool.Stats().Queued == 1 })
	if _, err := r.Pause(context.Background(), waiting); err != nil {
		t.Fatal(err)
	}
	release()
	eventually(t, "the slow check to be stored", func() bool { return e.rows(slow) == 1 })
	eventually(t, "the worker to be idle", func() bool { st := r.pool.Stats(); return st.Queued == 0 && st.Active == 0 })
	if tg.hits.Load() != 0 || e.rows(waiting) != 0 {
		t.Fatalf("the paused monitor was checked: %d requests, %d rows", tg.hits.Load(), e.rows(waiting))
	}
}

// A check cut off by shutdown is not a verdict about the target. Stored as
// a failure it would, with a threshold of one, mark the monitor DOWN on
// every restart.
func TestShutdownDropsACancelledCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	url, entered, _ := blockingTarget(t)
	id := e.monitor("slow", url, func(m *store.HTTPMonitor) { m.FailureThreshold = 1 })

	r := e.start()
	<-entered
	begin := time.Now()
	r.stop()
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("shutdown waited %v for a running check", took)
	}
	if m := e.get(id); m.State != "pending" || m.LastCheckAt != nil || e.rows(id) != 0 {
		t.Fatalf("after shutdown: %+v, %d rows", m, e.rows(id))
	}
}

// Results that are finished when shutdown starts are still stored.
func TestShutdownStoresFinishedResults(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("api", tg.URL, nil)

	r := e.start()
	// The result is in the processor's queue or batch (it flushes after
	// 200 ms), not yet in the database.
	eventually(t, "the check to run", func() bool { return tg.hits.Load() == 1 && r.pool.Stats().Active == 0 })
	r.stop()
	if m := e.get(id); m.State != "up" || e.rows(id) != 1 {
		t.Fatalf("after shutdown: %+v, %d rows", m, e.rows(id))
	}
}

// Stored configuration that cannot be used is shown as a failed check,
// not skipped in silence.
func TestUnusableConfigIsAFailedCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("broken", tg.URL, func(m *store.HTTPMonitor) { m.FailureThreshold = 1 })
	e.exec(`UPDATE http_monitor_config SET headers_json = 'nope' WHERE monitor_id = ?`, id)

	e.start()
	eventually(t, "the monitor to be down", func() bool { return e.get(id).State == "down" })
	var kind, msg string
	var status sql.NullString
	if err := e.d.Reader.QueryRow(`SELECT error_kind, error_message, protocol_status FROM check_results WHERE monitor_id = ?`, id).
		Scan(&kind, &msg, &status); err != nil {
		t.Fatal(err)
	}
	if kind != "unknown" || !strings.Contains(msg, "configuration") || status.Valid || tg.hits.Load() != 0 {
		t.Fatalf("kind %q, message %q, status %v, %d requests", kind, msg, status, tg.hits.Load())
	}
}

// A secret sealed for another row cannot be opened: also a failed check,
// and the message does not carry the stored bytes.
func TestUnreadableSecretIsAFailedCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("locked", tg.URL, func(m *store.HTTPMonitor) { m.FailureThreshold = 1 })
	other := e.monitor("other", tg.URL, func(m *store.HTTPMonitor) { m.Enabled = false })
	if err := store.SetSecret(context.Background(), e.d, e.key, other, "auth.bearer", []byte("s3cret-token"), created); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE monitor_secrets SET monitor_id = ? WHERE monitor_id = ?`, id, other)

	e.start()
	eventually(t, "the monitor to be down", func() bool { return e.get(id).State == "down" })
	var kind, msg string
	if err := e.d.Reader.QueryRow(`SELECT error_kind, error_message FROM check_results WHERE monitor_id = ?`, id).Scan(&kind, &msg); err != nil {
		t.Fatal(err)
	}
	if kind != "unknown" || !strings.Contains(msg, "secret") || strings.Contains(msg, "s3cret-token") || tg.hits.Load() != 0 {
		t.Fatalf("kind %q, message %q, %d requests", kind, msg, tg.hits.Load())
	}
}

// Secrets reach the request; the stored result does not contain them.
func TestCheckSendsSecrets(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Plain") + "|" + r.UserAgent())
	}))
	t.Cleanup(srv.Close)
	id := e.monitor("auth", srv.URL, func(m *store.HTTPMonitor) {
		m.Config.Headers = `[{"name":"X-Plain","value":"yes"}]`
	})
	if err := store.SetSecret(context.Background(), e.d, e.key, id, "auth.bearer", []byte("tok"), created); err != nil {
		t.Fatal(err)
	}

	e.start()
	eventually(t, "the monitor to be up", func() bool { return e.get(id).State == "up" })
	if got := auth.Load(); got != "Bearer tok|yes|Sinjal/test" {
		t.Fatalf("request carried %q", got)
	}
}

func TestStartFailsWhenMonitorsCannotBeRead(t *testing.T) {
	e := newEnv(t)
	e.d.Close()
	eng := New(e.d, e.key, 4, "Sinjal/test", time.UTC, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := eng.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded on a closed database")
	}
}

// Pause and resume from many goroutines must leave the schedule and the
// database agreeing: every enabled monitor scheduled, no paused one.
func TestConcurrentPauseAndResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	var ids []string
	for _, name := range []string{"a", "b", "c", "d"} {
		ids = append(ids, e.monitor(name, tg.URL, nil))
	}
	r := e.start()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 25 {
				id := ids[rand.IntN(len(ids))]
				var err error
				if rand.IntN(2) == 0 {
					_, err = r.Pause(context.Background(), id)
				} else {
					_, err = r.Resume(context.Background(), id)
				}
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	enabled := 0
	for _, id := range ids {
		m := e.get(id)
		if m.Enabled == (m.State == "paused") {
			t.Fatalf("%s: enabled %v in state %s", m.Name, m.Enabled, m.State)
		}
		if m.Enabled {
			enabled++
		}
	}
	eventually(t, "the schedule to match the database", func() bool { return r.sch.Len() == enabled })
	var open int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM monitor_pauses WHERE resumed_at IS NULL`).Scan(&open); err != nil || open != len(ids)-enabled {
		t.Fatalf("%d open pause intervals for %d paused monitors (%v)", open, len(ids)-enabled, err)
	}
	// Whatever is scheduled is still checked: resume everything and see
	// every monitor come up.
	for _, id := range ids {
		if _, err := r.Resume(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "every monitor to be up", func() bool {
		for _, id := range ids {
			if e.get(id).State != "up" {
				return false
			}
		}
		return true
	})
}

// Everything that changes what a monitor's row shows is announced once, for
// the live-update stream: a stored result, a pause, a resume. A call that
// changed nothing announces nothing.
func TestChangesAreAnnounced(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("api", tg.URL, nil)
	other := e.monitor("other", tg.URL, func(m *store.HTTPMonitor) { m.Enabled = false })

	r := e.start()
	eventually(t, "the first result to be announced", func() bool { return e.announced(id) == 1 })
	if m := e.get(id); m.State != "up" {
		t.Fatalf("announced before the result was stored: state %s", m.State)
	}

	if _, err := r.Pause(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := e.announced(id); n != 2 {
		t.Fatalf("%d announcements after the pause, want 2", n)
	}
	if _, err := r.Pause(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := e.announced(id); n != 2 {
		t.Fatalf("pausing a paused monitor was announced (%d)", n)
	}

	if _, err := r.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// The resume itself, then the result of its immediate check.
	eventually(t, "the resume and its check to be announced", func() bool { return e.announced(id) == 4 })
	if _, err := r.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.announced(id); n != 4 {
		t.Fatalf("resuming a running monitor was announced (%d)", n)
	}
	if n := e.announced(other); n != 0 {
		t.Fatalf("a monitor that never changed was announced %d times", n)
	}
}

func TestStartDelay(t *testing.T) {
	for _, n := range []int{1, 2, 100, 500, 501, 1000, 10000} {
		prev := time.Duration(-1)
		for i := range n {
			d := startDelay(i, n)
			if d <= prev || d >= startWindow {
				t.Fatalf("startDelay(%d, %d) = %v after %v", i, n, d, prev)
			}
			prev = d
		}
		if startDelay(0, n) != 0 {
			t.Fatalf("the first of %d monitors waits %v", n, startDelay(0, n))
		}
	}
	// Few monitors keep a fixed small gap; many share the window evenly.
	if got := startDelay(3, 100); got != 3*startStep {
		t.Fatalf("startDelay(3, 100) = %v", got)
	}
	if got := startDelay(500, 1000); got != startWindow/2 {
		t.Fatalf("startDelay(500, 1000) = %v", got)
	}
}

// Created and edited monitors join the schedule through Schedule; disabled,
// paused and deleted ones leave it.
func TestSchedule(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	r := e.start()
	ctx := context.Background()

	// Created after the start: checked at once.
	id := e.monitor("api", tg.URL, nil)
	if err := r.Schedule(ctx, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new monitor to be checked", func() bool { return e.get(id).State == "up" })
	eventually(t, "one scheduled monitor", func() bool { return r.sch.Len() == 1 })

	// An edit checks again at once, long before the 30 s interval.
	hits := tg.hits.Load()
	if err := r.Schedule(ctx, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a check after the edit", func() bool { return tg.hits.Load() > hits })

	// An edit of a paused monitor does not schedule it again.
	if _, err := r.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Schedule(ctx, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the paused monitor off the schedule", func() bool { return r.sch.Len() == 0 })

	// Created disabled: never scheduled.
	off := e.monitor("off", tg.URL, func(m *store.HTTPMonitor) { m.Enabled = false })
	if err := r.Schedule(ctx, off); err != nil {
		t.Fatal(err)
	}
	// A monitor that no longer exists is removed, not an error.
	if _, err := r.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMonitor(ctx, e.d, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Schedule(ctx, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "nothing scheduled", func() bool { return r.sch.Len() == 0 })
}

func TestDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	id := e.monitor("api", tg.URL, nil)
	e.exec(`UPDATE monitors SET interval_seconds = 1 WHERE id = ?`, id)
	r := e.start()
	eventually(t, "the monitor to be up", func() bool { return e.get(id).State == "up" })

	if err := r.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the monitor off the schedule", func() bool { return r.sch.Len() == 0 })
	if _, err := store.GetMonitor(context.Background(), e.d.Reader, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if n := e.rows(id); n != 0 {
		t.Fatalf("%d results left", n)
	}
	time.Sleep(100 * time.Millisecond) // a check already on its way may land
	hits := tg.hits.Load()
	time.Sleep(1300 * time.Millisecond)
	if got := tg.hits.Load(); got != hits {
		t.Fatalf("%d checks after the delete", got-hits)
	}
	if err := r.Delete(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// Pausing a monitor that has an active incident closes it, and that close
// is announced once; pausing one without an incident announces nothing.
func TestPauseAnnouncesTheClosedIncident(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tg := newTarget(t)
	down := e.monitor("down", tg.URL, nil)
	healthy := e.monitor("healthy", tg.URL, nil)
	e.exec(`INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES ('inc1', ?, ?, ?)`,
		down, store.FormatTime(created), store.FormatTime(created))

	var mu sync.Mutex
	var got []string
	e.incidents = func(event, incidentID, monitorID string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, event+" "+incidentID+" "+monitorID)
	}
	eng := e.start()

	for _, id := range []string{healthy, down, down} {
		if _, err := eng.Pause(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"incident.closed inc1 " + down}; !reflect.DeepEqual(got, want) {
		t.Fatalf("announced %q, want %q", got, want)
	}
}
