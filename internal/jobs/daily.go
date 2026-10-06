// Package jobs runs Sinjal's daily internal job (docs/09_DATABASE.md
// "Retention jobs"): once a day at an off-peak local hour, and at startup
// when the last run is overdue. It hosts the history rollup with the
// database maintenance after it and the expired-session cleanup.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/retention"
	"github.com/drilonrecica/sinjal/internal/store"
)

// RunHour is the local hour of the daily run. 04:00 is quiet for a
// personal instance and outside the hours DST skips or repeats in Europe
// and North America, so it happens exactly once every local day.
const RunHour = 4

// LastRunKey is the system_settings key holding the time of the last
// successful run (RFC 3339 UTC).
const LastRunKey = "last_retention_run"

// maxSleep bounds one wait, so a clock change or a suspended machine
// delays the run by at most this much.
const maxSleep = time.Hour

// Daily is the daily job.
type Daily struct {
	db       *db.DB
	sessions *auth.Sessions
	loc      *time.Location
	log      *slog.Logger
	now      func() time.Time
}

// NewDaily returns the daily job for the instance time zone loc.
func NewDaily(d *db.DB, sessions *auth.Sessions, loc *time.Location, logger *slog.Logger) *Daily {
	return &Daily{db: d, sessions: sessions, loc: loc, log: logging.Sub(logger, "jobs"), now: time.Now}
}

// Run catches up on an overdue run, then runs at every RunHour until ctx
// is cancelled. A run cut off by the cancellation stops between rollup
// steps and leaves consistent data.
func (j *Daily) Run(ctx context.Context) {
	if _, err := j.CatchUp(ctx); err != nil && ctx.Err() == nil {
		j.log.Error("reading the last daily run failed", "error", err)
	}
	next := NextRun(j.now(), j.loc)
	for {
		t := time.NewTimer(min(next.Sub(j.now()), maxSleep))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if now := j.now(); !now.Before(next) {
			j.RunOnce(ctx, now)
			next = NextRun(now, j.loc)
		}
	}
}

// CatchUp runs the job now if the last successful run is older than the
// latest scheduled time (or there was none), and reports whether it ran.
func (j *Daily) CatchUp(ctx context.Context) (bool, error) {
	now := j.now()
	v, ok, err := store.Setting(ctx, j.db.Reader, LastRunKey)
	if err != nil {
		return false, err
	}
	var last time.Time
	if ok {
		last, _ = time.Parse(time.RFC3339, v)
	}
	if !Overdue(last, now, j.loc) {
		return false, nil
	}
	return true, j.RunOnce(ctx, now)
}

// RunOnce runs the job as of now. The run is recorded only when the
// rollup succeeded, so a failed one is retried at the next start or the
// next day; its error is logged and returned.
func (j *Daily) RunOnce(ctx context.Context, now time.Time) error {
	start := time.Now()
	st, err := retention.Rollup(ctx, j.db, now)
	if err != nil {
		if ctx.Err() == nil {
			j.log.Error("history rollup failed; source rows of the failed step are kept",
				"error", err, "steps", st.Steps, "deleted", st.Deleted)
		}
		return err
	}
	j.log.Info("history rollup finished", "steps", st.Steps, "buckets", st.Buckets,
		"deleted", st.Deleted, "duration", time.Since(start).Round(time.Millisecond))
	// Refresh the planner's statistics for the tables the rollup changed.
	// SQLite bounds the work itself (a temporary analysis_limit), so this
	// is cheap. There is no VACUUM: freed pages are reused by the next
	// day's results (docs/09 "Retention jobs").
	if _, err := j.db.Writer.ExecContext(ctx, `PRAGMA optimize`); err != nil && ctx.Err() == nil {
		j.log.Error("PRAGMA optimize failed", "error", err)
	}
	// Expired sessions are already rejected on lookup; deleting them only
	// keeps the table small, so a failure is logged and nothing more.
	if n, err := j.sessions.DeleteExpired(ctx, now); err != nil {
		if ctx.Err() == nil {
			j.log.Error("expired session cleanup failed", "error", err)
		}
	} else if n > 0 {
		j.log.Info("deleted expired sessions", "count", n)
	}
	if err := store.SetSetting(ctx, j.db.Writer, LastRunKey, store.FormatTime(now), now); err != nil {
		j.log.Error("recording the daily run failed", "error", err)
		return err
	}
	return nil
}

// LastScheduled is the latest RunHour in loc at or before now.
func LastScheduled(now time.Time, loc *time.Location) time.Time {
	l := now.In(loc)
	t := time.Date(l.Year(), l.Month(), l.Day(), RunHour, 0, 0, 0, loc)
	if t.After(now) {
		t = time.Date(l.Year(), l.Month(), l.Day()-1, RunHour, 0, 0, 0, loc)
	}
	return t
}

// NextRun is the first RunHour in loc after now.
func NextRun(now time.Time, loc *time.Location) time.Time {
	l := LastScheduled(now, loc)
	return time.Date(l.Year(), l.Month(), l.Day()+1, RunHour, 0, 0, 0, loc)
}

// Overdue reports whether a job last run at last (zero: never) has missed
// a scheduled run by now.
func Overdue(last, now time.Time, loc *time.Location) bool {
	return last.IsZero() || last.Before(LastScheduled(now, loc))
}
