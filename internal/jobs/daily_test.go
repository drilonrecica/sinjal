package jobs

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func belgrade(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSchedule(t *testing.T) {
	bel := belgrade(t)
	local := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, bel)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		now, last, next string // local times in Belgrade
	}{
		{"2026-10-07 12:00", "2026-10-07 04:00", "2026-10-08 04:00"},
		{"2026-10-07 04:00", "2026-10-07 04:00", "2026-10-08 04:00"}, // exactly at the hour
		{"2026-10-07 03:59", "2026-10-06 04:00", "2026-10-07 04:00"},
		{"2026-01-01 00:30", "2025-12-31 04:00", "2026-01-01 04:00"},
		// CEST starts at 02:00 on 29 March, CET returns at 03:00 on 25
		// October: 04:00 exists once on both days, 23 or 25 hours apart.
		{"2026-03-29 01:00", "2026-03-28 04:00", "2026-03-29 04:00"},
		{"2026-03-29 05:00", "2026-03-29 04:00", "2026-03-30 04:00"},
		{"2026-10-25 02:30", "2026-10-24 04:00", "2026-10-25 04:00"},
	}
	for _, c := range cases {
		now := local(c.now)
		if got := LastScheduled(now, bel); !got.Equal(local(c.last)) {
			t.Errorf("LastScheduled(%s) = %v, want %s", c.now, got.In(bel), c.last)
		}
		if got := NextRun(now, bel); !got.Equal(local(c.next)) {
			t.Errorf("NextRun(%s) = %v, want %s", c.now, got.In(bel), c.next)
		}
	}
	if d := NextRun(local("2026-03-28 12:00"), bel).Sub(local("2026-03-28 04:00")); d != 23*time.Hour {
		t.Errorf("spring day lasts %v", d)
	}
	if d := NextRun(local("2026-10-24 12:00"), bel).Sub(local("2026-10-24 04:00")); d != 25*time.Hour {
		t.Errorf("autumn day lasts %v", d)
	}
}

func TestOverdue(t *testing.T) {
	bel := belgrade(t)
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC) // 12:00 in Belgrade; today's run was 02:00 UTC
	cases := []struct {
		name string
		last time.Time
		want bool
	}{
		{"never ran", time.Time{}, true},
		{"ran today after the hour", time.Date(2026, 10, 7, 2, 0, 5, 0, time.UTC), false},
		{"ran exactly at the hour", time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC), false},
		{"ran yesterday after the hour", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC), true},
		{"ran today before the hour", time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC), true},
	}
	for _, c := range cases {
		if got := Overdue(c.last, now, bel); got != c.want {
			t.Errorf("%s: overdue = %v, want %v", c.name, got, c.want)
		}
	}
}

type env struct {
	d        *db.DB
	job      *Daily
	sessions *auth.Sessions
	now      time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, sessions: auth.NewSessions(d, quiet), now: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	e.job = NewDaily(d, e.sessions, belgrade(t), quiet)
	e.job.now = func() time.Time { return e.now }
	return e
}

func (e *env) lastRun(t *testing.T) string {
	t.Helper()
	v, _, err := store.Setting(context.Background(), e.d.Reader, LastRunKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// seed stores a monitor with a raw result 10 days old and an expired
// session, both of which a run removes.
func (e *env) seed(t *testing.T) (monitorID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	id, err := store.CreateMonitor(ctx, e.d, store.MonitorInput{Name: "api", Enabled: true,
		HTTP: store.HTTPConfig{URL: "https://example.com/"}}, e.now.AddDate(0, -1, 0))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertCheckResult(ctx, tx, store.CheckResult{MonitorID: id, CheckedAt: e.now.AddDate(0, 0, -10),
		Success: true, Duration: 12 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Writer.Exec(`INSERT INTO users (id, login, role, created_at, updated_at)
		VALUES ('u1', 'admin', 'admin', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	_, s, err := e.sessions.Create(ctx, "u1", "", "", e.now.Add(-auth.SessionLifetime-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return id, s.ID
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Reader.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCatchUpRunsWhenOverdue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, session := e.seed(t)

	ran, err := e.job.CatchUp(ctx) // never ran
	if err != nil || !ran {
		t.Fatalf("first catch-up: ran %v, %v", ran, err)
	}
	if got := e.lastRun(t); got != "2026-10-07T10:00:00Z" {
		t.Errorf("last run %q", got)
	}
	if n := e.count(t, `SELECT count(*) FROM check_results WHERE monitor_id = ?`, id); n != 0 {
		t.Errorf("%d raw rows left", n)
	}
	if n := e.count(t, `SELECT count(*) FROM check_aggregates WHERE monitor_id = ?`, id); n != 1 {
		t.Errorf("%d aggregates", n)
	}
	if n := e.count(t, `SELECT count(*) FROM sessions WHERE id = ?`, session); n != 0 {
		t.Error("expired session kept")
	}

	// Later the same day nothing is due; the next day after 04:00 it is.
	e.now = e.now.Add(6 * time.Hour)
	if ran, err := e.job.CatchUp(ctx); err != nil || ran {
		t.Errorf("same day: ran %v, %v", ran, err)
	}
	e.now = time.Date(2026, 10, 8, 2, 30, 0, 0, time.UTC) // 04:30 in Belgrade
	if ran, err := e.job.CatchUp(ctx); err != nil || !ran {
		t.Errorf("next day: ran %v, %v", ran, err)
	}
	if got := e.lastRun(t); got != "2026-10-08T02:30:00Z" {
		t.Errorf("last run %q", got)
	}
}

func TestFailedRunIsNotRecorded(t *testing.T) {
	e := newEnv(t)
	id, _ := e.seed(t)
	if _, err := e.d.Writer.Exec(`CREATE TRIGGER fail BEFORE INSERT ON check_aggregates
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.job.CatchUp(context.Background()); !ran || err == nil {
		t.Fatalf("ran %v, err %v; want a failed run", ran, err)
	}
	if got := e.lastRun(t); got != "" {
		t.Errorf("failed run recorded at %q", got)
	}
	if n := e.count(t, `SELECT count(*) FROM check_results WHERE monitor_id = ?`, id); n != 1 {
		t.Errorf("%d raw rows after a failed run, want 1", n)
	}
}

func TestRunCatchesUpAndStops(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.job.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.lastRun(t) == "" {
		if time.Now().After(deadline) {
			t.Fatal("overdue run not done at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
