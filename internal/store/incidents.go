package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/incident"
)

// The functions in this file write incidents inside the caller's
// transaction, so that an incident opens or closes together with the state
// change that causes it (docs/10_INCIDENTS.md): the result processor's
// batch, or a pause.

// NewIncident is an outage at the moment it is confirmed.
type NewIncident struct {
	MonitorID   string
	StartedAt   time.Time // the first qualifying failure
	DeclaredAt  time.Time // the check that met the failure threshold
	FailureKind string    // of the first failure
	Detected    string    // message of the first failure
	Summary     string    // message of the failure that confirmed it
}

// OpenIncident records a monitor's active incident with its detected and
// declared_down events and returns its id. A monitor has at most one active
// incident: if there is one already, nothing is written, its id is returned
// and opened is false.
func OpenIncident(ctx context.Context, tx *sql.Tx, in NewIncident) (id string, opened bool, err error) {
	id = ids.New()
	res, err := tx.ExecContext(ctx, `INSERT INTO incidents
		(id, monitor_id, started_at, initial_failure_kind, summary, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (monitor_id) WHERE ended_at IS NULL DO NOTHING`,
		id, in.MonitorID, formatTime(in.StartedAt), nullStr(in.FailureKind), nullStr(in.Summary), formatTime(in.DeclaredAt))
	if err != nil {
		return "", false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		id, err := ActiveIncidentID(ctx, tx, in.MonitorID)
		return id, false, err
	}
	if err := AddIncidentEvent(ctx, tx, id, incident.EventDetected, in.Detected, in.StartedAt); err != nil {
		return "", false, err
	}
	if err := AddIncidentEvent(ctx, tx, id, incident.EventDeclaredDown, in.Summary, in.DeclaredAt); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// CloseIncident ends a monitor's active incident at the given time and
// records why as an event of the given type (recovered, paused) whose
// message is the outage's duration. It returns the incident's id, or ok
// false, having written nothing, when the monitor has no active incident.
func CloseIncident(ctx context.Context, tx *sql.Tx, monitorID string, at time.Time, eventType string) (id string, ok bool, err error) {
	var started string
	err = tx.QueryRowContext(ctx, `UPDATE incidents SET ended_at = ? WHERE monitor_id = ? AND ended_at IS NULL
		RETURNING id, started_at`, formatTime(at), monitorID).Scan(&id, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	down := parseTime(formatTime(at)).Sub(parseTime(started))
	if err := AddIncidentEvent(ctx, tx, id, eventType, "down for "+down.String(), at); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// AddIncidentEvent appends one entry to an incident's timeline.
func AddIncidentEvent(ctx context.Context, tx *sql.Tx, incidentID, eventType, message string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO incident_events (incident_id, event_type, message, created_at)
		VALUES (?, ?, ?, ?)`, incidentID, eventType, nullStr(message), formatTime(at))
	return err
}

// ActiveIncidentID returns the id of a monitor's active incident, or "" when
// it has none.
func ActiveIncidentID(ctx context.Context, tx *sql.Tx, monitorID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, monitorID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// RecentTransitions returns the times of a monitor's latest confirmed
// state transitions, newest first: an incident's start (UP to DOWN) and its
// end (DOWN to UP), unless a pause ended it, which is no recovery. It reads
// the monitor's last incident.FlapTransitions incidents, which hold at
// least that many transitions if the monitor has had them. This is the
// whole flapping window: nothing else remembers transitions, so it is the
// same before and after a restart (docs/10 "Flapping").
func RecentTransitions(ctx context.Context, tx *sql.Tx, monitorID string) ([]time.Time, error) {
	rows, err := tx.QueryContext(ctx, `SELECT started_at, ended_at,
		EXISTS (SELECT 1 FROM incident_events e WHERE e.incident_id = i.id AND e.event_type = ?)
		FROM incidents i WHERE monitor_id = ? ORDER BY started_at DESC LIMIT ?`,
		incident.EventPaused, monitorID, incident.FlapTransitions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var started string
		var ended sql.NullString
		var paused bool
		if err := rows.Scan(&started, &ended, &paused); err != nil {
			return nil, err
		}
		if ended.Valid && !paused {
			out = append(out, parseTime(ended.String))
		}
		out = append(out, parseTime(started))
	}
	return out, rows.Err()
}

// SetFlapping sets the FLAPPING overlay of a monitor to since, or clears it
// when since is nil.
func SetFlapping(ctx context.Context, tx *sql.Tx, monitorID string, since *time.Time) error {
	var v any
	if since != nil {
		v = formatTime(*since)
	}
	_, err := tx.ExecContext(ctx, `UPDATE monitors SET flapping_since = ? WHERE id = ?`, v, monitorID)
	return err
}
