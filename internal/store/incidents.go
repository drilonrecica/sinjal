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
	// SuppressedByParent: the parent monitor was down when it opened.
	SuppressedByParent bool
	// MaintenanceOverlap: a maintenance window covering the monitor was in
	// effect between its start and its confirmation.
	MaintenanceOverlap bool
}

// OpenIncident records a monitor's active incident with its detected and
// declared_down events and returns its id. A monitor has at most one active
// incident: if there is one already, nothing is written, its id is returned
// and opened is false.
func OpenIncident(ctx context.Context, tx *sql.Tx, in NewIncident) (id string, opened bool, err error) {
	id = ids.New()
	res, err := tx.ExecContext(ctx, `INSERT INTO incidents
		(id, monitor_id, started_at, initial_failure_kind, summary, suppressed_by_parent, maintenance_overlap, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (monitor_id) WHERE ended_at IS NULL DO NOTHING`,
		id, in.MonitorID, formatTime(in.StartedAt), nullStr(in.FailureKind), nullStr(in.Summary),
		b2i(in.SuppressedByParent), b2i(in.MaintenanceOverlap), formatTime(in.DeclaredAt))
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

// ParentDown reports whether the monitor with the given id is DOWN. It
// reads inside tx, so a parent that went down earlier in the same batch
// counts. A missing parent is not down.
func ParentDown(ctx context.Context, tx *sql.Tx, parentID string) (bool, error) {
	var down bool
	err := tx.QueryRowContext(ctx, `SELECT current_state = 'down' FROM monitors WHERE id = ?`, parentID).Scan(&down)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return down, err
}

// PendingDown returns the monitor's active incident if its DOWN
// notification is still held back by the parent or by maintenance: the
// latest decision about it is such a suppression, not yet followed by
// notification_resumed. It returns "" otherwise. A DOWN held back by
// flapping is not pending; the end of the flapping decides it.
func PendingDown(ctx context.Context, tx *sql.Tx, monitorID string) (string, error) {
	var id, typ, msg string
	err := tx.QueryRowContext(ctx, `SELECT i.id, e.event_type, coalesce(e.message, '')
		FROM incidents i JOIN incident_events e ON e.incident_id = i.id
		WHERE i.monitor_id = ? AND i.ended_at IS NULL
		  AND (e.event_type = ? OR (e.event_type = ? AND e.message LIKE 'down: %'))
		ORDER BY e.id DESC LIMIT 1`,
		monitorID, incident.EventNotificationResumed, incident.EventNotificationSuppressed).Scan(&id, &typ, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if typ == incident.EventNotificationSuppressed &&
		(msg == "down: "+string(incident.ByParent) || msg == "down: "+string(incident.ByMaintenance)) {
		return id, nil
	}
	return "", nil
}

// Reminder is a monitor's active incident whose outage reminder has not
// been decided yet, and when it falls due.
type Reminder struct {
	IncidentID string
	Due        time.Time // the incident's start plus the profile's reminder duration
}

// PendingReminder returns the reminder of the monitor's active incident if
// its profile asks for one and it has not been decided; ok is false
// otherwise. The duration is read from the profile as it is now, so a
// change applies to running incidents too.
func PendingReminder(ctx context.Context, tx *sql.Tx, monitorID string) (r Reminder, ok bool, err error) {
	var started string
	var after int64
	err = tx.QueryRowContext(ctx, `SELECT i.id, i.started_at, p.reminder_after_seconds
		FROM incidents i
		JOIN monitors m ON m.id = i.monitor_id
		JOIN notification_profiles p ON p.id = m.notification_profile_id
		WHERE i.monitor_id = ? AND i.ended_at IS NULL AND i.reminder_sent_at IS NULL
		  AND p.reminder_after_seconds > 0`, monitorID).Scan(&r.IncidentID, &started, &after)
	if errors.Is(err, sql.ErrNoRows) {
		return Reminder{}, false, nil
	}
	if err != nil {
		return Reminder{}, false, err
	}
	r.Due = parseTime(started).Add(time.Duration(after) * time.Second)
	return r, true, nil
}

// MarkReminder records that an incident's reminder was decided, so it is
// decided once (docs/11 "Outage reminder"). It reports false when it had
// been already.
func MarkReminder(ctx context.Context, tx *sql.Tx, incidentID string, at time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE incidents SET reminder_sent_at = ? WHERE id = ? AND reminder_sent_at IS NULL`,
		formatTime(at), incidentID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// IncidentStart returns when an incident started.
func IncidentStart(ctx context.Context, tx *sql.Tx, incidentID string) (time.Time, error) {
	var started string
	err := tx.QueryRowContext(ctx, `SELECT started_at FROM incidents WHERE id = ?`, incidentID).Scan(&started)
	return parseTime(started), err
}

// SetMaintenanceOverlap marks an incident as having overlapped a
// maintenance window.
func SetMaintenanceOverlap(ctx context.Context, tx *sql.Tx, incidentID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE incidents SET maintenance_overlap = 1 WHERE id = ?`, incidentID)
	return err
}
