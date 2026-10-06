package store

import (
	"context"
	"database/sql"
)

// MaxRecentFailures is how many failed checks the Diagnostics tab lists.
const MaxRecentFailures = 20

// RecentFailures returns a monitor's newest failed checks, newest first, at
// most limit. It walks idx_check_results_monitor_time from the newest
// result and stops after limit failures.
func RecentFailures(ctx context.Context, q *sql.DB, monitorID string, limit int) ([]CheckResult, error) {
	rows, err := q.QueryContext(ctx, `SELECT checked_at, duration_ms, protocol_status, error_kind, error_message,
		diagnostic_snippet FROM check_results WHERE monitor_id = ? AND success = 0
		ORDER BY checked_at DESC, id DESC LIMIT ?`, monitorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		var at string
		var ms sql.NullFloat64
		var status, kind, msg, snippet sql.NullString
		if err := rows.Scan(&at, &ms, &status, &kind, &msg, &snippet); err != nil {
			return nil, err
		}
		out = append(out, CheckResult{
			MonitorID: monitorID, CheckedAt: parseTime(at), Duration: msToDuration(ms.Float64),
			ProtocolStatus: status.String, ErrorKind: kind.String, ErrorMessage: msg.String, Snippet: snippet.String,
		})
	}
	return out, rows.Err()
}
