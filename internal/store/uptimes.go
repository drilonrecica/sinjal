package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// UptimesRaw returns the raw uptime of every monitor over [from, to) as of
// now: the monitor list column's figure, the detail header's raw one for
// each. Three statements in one read transaction (monitors, then the
// pauses and incidents overlapping the range), instead of three per
// monitor: Uptime costs about 140 µs a monitor, 140 ms for 1,000 (docs/18).
// Maintenance only changes adjusted uptime, so it is not read.
func UptimesRaw(ctx context.Context, q *sql.DB, from, to, now time.Time) (map[string]incident.Ratio, error) {
	tx, err := q.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	end := to
	if now.Before(end) {
		end = now
	}

	created := map[string]time.Time{}
	rows, err := tx.QueryContext(ctx, `SELECT id, created_at FROM monitors`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		created[id] = parseTime(at)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	byMonitor := func(query string) (map[string][]incident.Span, error) {
		rows, err := tx.QueryContext(ctx, query, formatTime(end), formatTime(from))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string][]incident.Span{}
		for rows.Next() {
			var id, start string
			var stop sql.NullString
			if err := rows.Scan(&id, &start, &stop); err != nil {
				return nil, err
			}
			s := incident.Span{From: parseTime(start)}
			if stop.Valid {
				s.To = parseTime(stop.String)
			}
			out[id] = append(out[id], s)
		}
		return out, rows.Err()
	}
	paused, err := byMonitor(`SELECT monitor_id, paused_at, resumed_at FROM monitor_pauses
		WHERE paused_at < ? AND (resumed_at IS NULL OR resumed_at > ?)`)
	if err != nil {
		return nil, err
	}
	down, err := byMonitor(`SELECT monitor_id, started_at, ended_at FROM incidents
		WHERE started_at < ? AND (ended_at IS NULL OR ended_at > ?)`)
	if err != nil {
		return nil, err
	}

	out := make(map[string]incident.Ratio, len(created))
	for id, c := range created {
		out[id], _ = incident.Uptime(incident.UptimeInput{Range: incident.Span{From: from, To: to}, Created: c, Now: now,
			Paused: paused[id], Down: down[id]})
	}
	return out, nil
}

// MonitorNames returns the names of the monitors with the given ids that
// exist, by primary key.
func MonitorNames(ctx context.Context, q querier, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]byte, 0, 2*len(ids))
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
	}
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM monitors WHERE id IN (`+string(marks)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
