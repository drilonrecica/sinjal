package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// PauseMonitor pauses a monitor at now (docs/10 "Pausing"): the state
// becomes paused, the FLAPPING overlay is cleared and a pause interval is
// opened, which uptime excludes. It reports false, and changes nothing, when
// the monitor is already paused.
func PauseMonitor(ctx context.Context, d *db.DB, id string, now time.Time) (bool, error) {
	ts := formatTime(now)
	return setPaused(ctx, d, id, `UPDATE monitors SET enabled = 0, current_state = 'paused',
		current_state_since = ?, flapping_since = NULL, updated_at = ?
		WHERE id = ? AND current_state != 'paused'`,
		`INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES (?, ?)`, ts)
}

// ResumeMonitor resumes a paused monitor at now: the open pause interval is
// closed and the state is pending until the first check result. A monitor
// that was created disabled has no interval to close. It reports false, and
// changes nothing, when the monitor is not paused.
func ResumeMonitor(ctx context.Context, d *db.DB, id string, now time.Time) (bool, error) {
	ts := formatTime(now)
	return setPaused(ctx, d, id, `UPDATE monitors SET enabled = 1, current_state = 'pending',
		current_state_since = ?, updated_at = ?
		WHERE id = ? AND current_state = 'paused'`,
		`UPDATE monitor_pauses SET resumed_at = ?2 WHERE monitor_id = ?1 AND resumed_at IS NULL`, ts)
}

// setPaused runs a guarded state change and, when it applied, the matching
// change to monitor_pauses (arguments: monitor id, timestamp), in one
// transaction.
func setPaused(ctx context.Context, d *db.DB, id, update, interval, ts string) (bool, error) {
	changed := false
	err := db.Retry(ctx, func() error {
		changed = false
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, update, ts, ts, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM monitors WHERE id = ?`, id).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, interval, id, ts); err != nil {
			return err
		}
		changed = true
		return tx.Commit()
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// Schedule is what the scheduler needs to know about a monitor.
type Schedule struct {
	ID       string
	Interval time.Duration
}

// ListSchedules returns the monitors that are checked: every enabled one,
// by id. It reads neither configuration nor secrets.
func ListSchedules(ctx context.Context, q *sql.DB) ([]Schedule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, interval_seconds FROM monitors WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var s Schedule
		var seconds int
		if err := rows.Scan(&s.ID, &seconds); err != nil {
			return nil, err
		}
		s.Interval = time.Duration(seconds) * time.Second
		out = append(out, s)
	}
	return out, rows.Err()
}
