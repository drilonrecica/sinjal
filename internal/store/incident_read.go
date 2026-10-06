package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
)

// IncidentRow is an incident as the pages list it.
type IncidentRow struct {
	ID                 string
	MonitorID          string
	MonitorName        string
	StartedAt          time.Time
	EndedAt            *time.Time // nil while it is active
	FailureKind        string
	Summary            string
	SuppressedByParent bool
	MaintenanceOverlap bool
}

// IncidentEvent is one entry of an incident's timeline.
type IncidentEvent struct {
	Type    string
	Message string
	At      time.Time
}

const incidentColumns = `i.id, i.monitor_id, m.name, i.started_at, i.ended_at,
	COALESCE(i.initial_failure_kind, ''), COALESCE(i.summary, ''), i.suppressed_by_parent, i.maintenance_overlap`

func scanIncident(sc interface{ Scan(...any) error }) (IncidentRow, error) {
	var r IncidentRow
	var started string
	var ended sql.NullString
	if err := sc.Scan(&r.ID, &r.MonitorID, &r.MonitorName, &started, &ended, &r.FailureKind, &r.Summary,
		&r.SuppressedByParent, &r.MaintenanceOverlap); err != nil {
		return r, err
	}
	r.StartedAt = parseTime(started)
	r.EndedAt = parseNullTime(ended)
	return r, nil
}

// ListIncidents returns the active incidents, newest first, then up to
// limit ended ones, newest first. monitorID narrows it to one monitor, ""
// means all. Active incidents are read through the partial index of
// "one active incident per monitor", so they are never more than the
// monitors; the ended ones need a top-N pass over the incidents (or, for
// one monitor, its (monitor_id, started_at) index).
func ListIncidents(ctx context.Context, q querier, monitorID string, limit int) ([]IncidentRow, error) {
	var out []IncidentRow
	for _, ended := range []bool{false, true} {
		query := `SELECT ` + incidentColumns + ` FROM incidents i JOIN monitors m ON m.id = i.monitor_id WHERE `
		args := []any{}
		if ended {
			query += `i.ended_at IS NOT NULL`
		} else {
			query += `i.ended_at IS NULL`
		}
		if monitorID != "" {
			query += ` AND i.monitor_id = ?`
			args = append(args, monitorID)
		}
		query += ` ORDER BY i.started_at DESC`
		if ended {
			query += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r, err := scanIncident(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// GetIncident returns an incident with its timeline in the order it was
// written, or ErrNotFound.
func GetIncident(ctx context.Context, q querier, id string) (IncidentRow, []IncidentEvent, error) {
	r, err := scanIncident(q.QueryRowContext(ctx, `SELECT `+incidentColumns+`
		FROM incidents i JOIN monitors m ON m.id = i.monitor_id WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil, ErrNotFound
	}
	if err != nil {
		return r, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT event_type, COALESCE(message, ''), created_at
		FROM incident_events WHERE incident_id = ? ORDER BY id`, id)
	if err != nil {
		return r, nil, err
	}
	defer rows.Close()
	var events []IncidentEvent
	for rows.Next() {
		var e IncidentEvent
		var at string
		if err := rows.Scan(&e.Type, &e.Message, &at); err != nil {
			return r, nil, err
		}
		e.At = parseTime(at)
		events = append(events, e)
	}
	return r, events, rows.Err()
}

// AddIncidentNote appends a manual note to an incident's timeline at now
// and returns the incident's monitor, or ErrNotFound. A note may go on an
// ended incident too.
func AddIncidentNote(ctx context.Context, d *db.DB, id, message string, now time.Time) (monitorID string, err error) {
	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		err = tx.QueryRowContext(ctx, `SELECT monitor_id FROM incidents WHERE id = ?`, id).Scan(&monitorID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := AddIncidentEvent(ctx, tx, id, incident.EventManualNote, message, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	return monitorID, err
}
