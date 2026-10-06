package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
)

// PauseMonitor pauses a monitor at now (docs/10 "Pausing"): the state
// becomes paused, the FLAPPING overlay is cleared, a pause interval is
// opened, which uptime excludes, and the active incident, if there is one,
// ends with a paused event. It reports false, and changes nothing, when the
// monitor is already paused.
func PauseMonitor(ctx context.Context, d *db.DB, id string, now time.Time) (bool, error) {
	ts := formatTime(now)
	return setPaused(ctx, d, id, ts, `UPDATE monitors SET enabled = 0, current_state = 'paused',
		current_state_since = ?, flapping_since = NULL, updated_at = ?
		WHERE id = ? AND current_state != 'paused'`,
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES (?, ?)`, id, ts); err != nil {
				return err
			}
			_, _, err := CloseIncident(ctx, tx, id, now, incident.EventPaused)
			return err
		})
}

// ResumeMonitor resumes a paused monitor at now: the open pause interval is
// closed and the state is pending until the first check result. It reports false, and
// changes nothing, when the monitor is not paused.
func ResumeMonitor(ctx context.Context, d *db.DB, id string, now time.Time) (bool, error) {
	ts := formatTime(now)
	return setPaused(ctx, d, id, ts, `UPDATE monitors SET enabled = 1, current_state = 'pending',
		current_state_since = ?, updated_at = ?
		WHERE id = ? AND current_state = 'paused'`,
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE monitor_pauses SET resumed_at = ? WHERE monitor_id = ? AND resumed_at IS NULL`, ts, id)
			return err
		})
}

// setPaused runs a guarded state change (arguments: timestamp twice,
// monitor id) and, when it applied, what belongs to it, in one transaction.
func setPaused(ctx context.Context, d *db.DB, id, ts, update string, then func(tx *sql.Tx) error) (bool, error) {
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
		if err := then(tx); err != nil {
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
	ID string
	// Interval is the check interval; for a heartbeat monitor it is the
	// period it may go without a beat.
	Interval time.Duration
	// Deadline is set for a heartbeat monitor only: when it is late
	// (HeartbeatConfig.Deadline). Its first job runs then.
	Deadline time.Time
}

const scheduleQuery = `SELECT m.id, m.type, m.interval_seconds,
	h.expected_interval_seconds, h.grace_seconds, h.last_beat_at, ` + watchedSince + `
	FROM monitors m LEFT JOIN heartbeat_monitor_config h ON h.monitor_id = m.id`

func scanSchedule(r scanner) (Schedule, error) {
	var s Schedule
	var typ, watched string
	var seconds int
	var interval, grace sql.NullInt64
	var last sql.NullString
	if err := r.Scan(&s.ID, &typ, &seconds, &interval, &grace, &last, &watched); err != nil {
		return Schedule{}, err
	}
	s.Interval = time.Duration(seconds) * time.Second
	// A heartbeat monitor without its configuration keeps the plain
	// interval; its check reports the missing configuration.
	if typ == "heartbeat" && interval.Valid {
		c := HeartbeatConfig{
			ExpectedInterval: time.Duration(interval.Int64) * time.Second,
			Grace:            time.Duration(grace.Int64) * time.Second,
			LastBeatAt:       parseNullTime(last),
			WatchedSince:     parseTime(watched),
		}
		s.Interval = c.Period()
		s.Deadline = c.Deadline()
	}
	return s, nil
}

// GetSchedule returns the schedule of one monitor, enabled or not.
func GetSchedule(ctx context.Context, q *sql.DB, id string) (Schedule, error) {
	s, err := scanSchedule(q.QueryRowContext(ctx, scheduleQuery+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return s, err
}

// ListSchedules returns the monitors that are checked: every enabled one,
// by id. It reads neither configuration nor secrets, apart from the timing
// of heartbeat monitors.
func ListSchedules(ctx context.Context, q *sql.DB) ([]Schedule, error) {
	rows, err := q.QueryContext(ctx, scheduleQuery+` WHERE m.enabled = 1 ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
