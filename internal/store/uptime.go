package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// Intervals are what happened to a monitor within a range: its pauses and
// incidents overlapping it (an open one has a zero To and ends at now),
// and the occurrences of the maintenance windows covering it, clipped to
// the range and expanded in the instance time zone. Excluded are those of
// the windows excluded from adjusted uptime.
type Intervals struct {
	Created     time.Time
	Paused      []incident.Span
	Down        []incident.Span
	Maintenance []incident.Span
	Excluded    []incident.Span
}

// GetIntervals reads a monitor's intervals over [from, to) as of now, or
// ErrNotFound. Pauses and incidents are read through their
// (monitor_id, start) indexes.
func GetIntervals(ctx context.Context, q querier, monitorID string, from, to, now time.Time, loc *time.Location) (Intervals, error) {
	var iv Intervals
	var created string
	err := q.QueryRowContext(ctx, `SELECT created_at FROM monitors WHERE id = ?`, monitorID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return iv, ErrNotFound
	}
	if err != nil {
		return iv, err
	}
	iv.Created = parseTime(created)
	end := to
	if now.Before(end) {
		end = now
	}
	if iv.Paused, err = spans(ctx, q, `SELECT paused_at, resumed_at FROM monitor_pauses
		WHERE monitor_id = ? AND paused_at < ? AND (resumed_at IS NULL OR resumed_at > ?)`, monitorID, end, from); err != nil {
		return iv, err
	}
	if iv.Down, err = spans(ctx, q, `SELECT started_at, ended_at FROM incidents
		WHERE monitor_id = ? AND started_at < ? AND (ended_at IS NULL OR ended_at > ?)`, monitorID, end, from); err != nil {
		return iv, err
	}
	windows, err := MonitorWindows(ctx, q, monitorID)
	if err != nil {
		return iv, err
	}
	for _, w := range windows {
		for _, o := range w.Occurrences(from, end, loc) {
			s := incident.Span{From: o.From, To: o.To}
			iv.Maintenance = append(iv.Maintenance, s)
			if w.ExcludeUptime {
				iv.Excluded = append(iv.Excluded, s)
			}
		}
	}
	return iv, nil
}

// Uptime returns a monitor's raw and adjusted uptime over [from, to) as of
// now (docs/10_INCIDENTS.md "Uptime"). It reads intervals only, never raw
// check results, so the answer does not depend on how much of them
// retention has kept, and there is nothing to union with aggregates.
func Uptime(ctx context.Context, q querier, monitorID string, from, to, now time.Time, loc *time.Location) (raw, adjusted incident.Ratio, err error) {
	iv, err := GetIntervals(ctx, q, monitorID, from, to, now, loc)
	if err != nil {
		return raw, adjusted, err
	}
	raw, adjusted = UptimeOf(iv, from, to, now)
	return raw, adjusted, nil
}

// UptimeOf is the uptime of intervals already read.
func UptimeOf(iv Intervals, from, to, now time.Time) (raw, adjusted incident.Ratio) {
	return incident.Uptime(incident.UptimeInput{Range: incident.Span{From: from, To: to}, Created: iv.Created, Now: now,
		Paused: iv.Paused, Down: iv.Down, Excluded: iv.Excluded})
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
