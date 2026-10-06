package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The latest-result readers serve the monitor list and detail header. They
// go through idx_check_results_monitor_time, one index probe per monitor,
// never a scan of check_results.

// lastDurationSQL is the duration of a monitor's newest result, NULL when it
// has none or the newest one recorded no duration.
const lastDurationSQL = `(SELECT duration_ms FROM check_results
	WHERE monitor_id = m.id ORDER BY checked_at DESC, id DESC LIMIT 1)`

// LastDuration returns the duration of a monitor's newest check result.
// ok is false when there is no result yet or it recorded no duration.
func LastDuration(ctx context.Context, q *sql.DB, monitorID string) (d time.Duration, ok bool, err error) {
	var ms sql.NullFloat64
	err = q.QueryRowContext(ctx, `SELECT `+lastDurationSQL+` FROM monitors m WHERE m.id = ?`, monitorID).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return msToDuration(ms.Float64), ms.Valid, err
}

// LastDurations is LastDuration for every monitor in one query. Monitors
// without a duration are absent from the map.
func LastDurations(ctx context.Context, q *sql.DB) (map[string]time.Duration, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id, `+lastDurationSQL+` FROM monitors m`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Duration{}
	for rows.Next() {
		var id string
		var ms sql.NullFloat64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		if ms.Valid {
			out[id] = msToDuration(ms.Float64)
		}
	}
	return out, rows.Err()
}

func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
