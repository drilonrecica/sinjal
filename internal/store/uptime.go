package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// Uptime returns a monitor's raw and adjusted uptime over [from, to) as of
// now (docs/10_INCIDENTS.md "Uptime"). It reads intervals only: the
// monitor's creation, its pauses and incidents overlapping the range
// (indexed by monitor and start), and the maintenance windows covering it
// that are excluded from adjusted uptime, expanded in loc. Raw check
// results are not read, so the answer does not depend on how much of them
// retention has kept, and there is nothing to union with aggregates.
func Uptime(ctx context.Context, q querier, monitorID string, from, to, now time.Time, loc *time.Location) (raw, adjusted incident.Ratio, err error) {
	in := incident.UptimeInput{Range: incident.Span{From: from, To: to}, Now: now}
	var created string
	err = q.QueryRowContext(ctx, `SELECT created_at FROM monitors WHERE id = ?`, monitorID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return raw, adjusted, ErrNotFound
	}
	if err != nil {
		return raw, adjusted, err
	}
	in.Created = parseTime(created)
	end := to
	if now.Before(end) {
		end = now
	}
	if in.Paused, err = spans(ctx, q, `SELECT paused_at, resumed_at FROM monitor_pauses
		WHERE monitor_id = ? AND paused_at < ? AND (resumed_at IS NULL OR resumed_at > ?)`, monitorID, end, from); err != nil {
		return raw, adjusted, err
	}
	if in.Down, err = spans(ctx, q, `SELECT started_at, ended_at FROM incidents
		WHERE monitor_id = ? AND started_at < ? AND (ended_at IS NULL OR ended_at > ?)`, monitorID, end, from); err != nil {
		return raw, adjusted, err
	}
	windows, err := MonitorWindows(ctx, q, monitorID)
	if err != nil {
		return raw, adjusted, err
	}
	for _, w := range windows {
		if !w.ExcludeUptime {
			continue
		}
		for _, iv := range w.Occurrences(from, end, loc) {
			in.Excluded = append(in.Excluded, incident.Span{From: iv.From, To: iv.To})
		}
	}
	raw, adjusted = incident.Uptime(in)
	return raw, adjusted, nil
}

// spans reads (start, end) rows overlapping [from, to); a NULL end is an
// open span.
func spans(ctx context.Context, q querier, query, monitorID string, to, from time.Time) ([]incident.Span, error) {
	rows, err := q.QueryContext(ctx, query, monitorID, formatTime(to), formatTime(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []incident.Span
	for rows.Next() {
		var start string
		var end sql.NullString
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		s := incident.Span{From: parseTime(start)}
		if end.Valid {
			s.To = parseTime(end.String)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
