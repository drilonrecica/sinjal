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
		err := tx.QueryRowContext(ctx, `SELECT id FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, in.MonitorID).Scan(&id)
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
