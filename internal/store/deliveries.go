package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/incident"
)

// The functions in this file are what the notification dispatcher reads
// and writes (docs/11_NOTIFICATIONS.md "Dispatcher"): where a notification
// goes, what it says, and what became of each attempt.

// NotificationTarget is the monitor a notification is about, with the
// profile that routes it.
type NotificationTarget struct {
	MonitorName string
	MonitorType string
	TLSNotAfter *time.Time // the certificate expiry last seen, if any

	ProfileID      string // "" when the monitor has no profile: nothing is sent
	QuietEnabled   bool
	QuietStart     string // "HH:MM" in the instance time zone
	QuietEnd       string
	CriticalBypass bool // critical notifications ignore quiet hours
}

// GetNotificationTarget returns the monitor and its notification profile,
// or ErrNotFound when the monitor is gone.
func GetNotificationTarget(ctx context.Context, q querier, monitorID string) (NotificationTarget, error) {
	var t NotificationTarget
	var tls sql.NullString
	err := q.QueryRowContext(ctx, `SELECT m.name, m.type, m.tls_not_after, COALESCE(p.id, ''),
		COALESCE(p.quiet_hours_enabled, 0), COALESCE(p.quiet_start, ''), COALESCE(p.quiet_end, ''),
		COALESCE(p.critical_bypass, 1)
		FROM monitors m LEFT JOIN notification_profiles p ON p.id = m.notification_profile_id
		WHERE m.id = ?`, monitorID).Scan(&t.MonitorName, &t.MonitorType, &tls, &t.ProfileID,
		&t.QuietEnabled, &t.QuietStart, &t.QuietEnd, &t.CriticalBypass)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.TLSNotAfter = parseNullTime(tls)
	return t, err
}

// RouteChannel is a channel a profile routes a severity to.
type RouteChannel struct {
	ID   string
	Name string
}

// RouteChannels returns the enabled channels a profile sends notifications
// of the given severity to, by name.
func RouteChannels(ctx context.Context, q querier, profileID, severity string) ([]RouteChannel, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name FROM notification_routes r
		JOIN notification_channels c ON c.id = r.channel_id
		WHERE r.profile_id = ? AND r.severity = ? AND c.enabled = 1
		ORDER BY c.name COLLATE NOCASE, c.id`, profileID, severity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteChannel
	for rows.Next() {
		var c RouteChannel
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IncidentFacts is what a notification says about an incident.
type IncidentFacts struct {
	StartedAt time.Time
	EndedAt   *time.Time // nil while it is active
	Summary   string     // the failure that confirmed the outage
	// Attempts is how many checks failed from the first failure to the one
	// that confirmed the outage.
	Attempts int
}

// GetIncidentFacts returns an incident's facts, or ErrNotFound.
func GetIncidentFacts(ctx context.Context, q querier, incidentID string) (IncidentFacts, error) {
	var f IncidentFacts
	var monitorID, started, declared string
	var ended sql.NullString
	err := q.QueryRowContext(ctx, `SELECT monitor_id, started_at, ended_at, COALESCE(summary, ''), created_at
		FROM incidents WHERE id = ?`, incidentID).Scan(&monitorID, &started, &ended, &f.Summary, &declared)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	f.StartedAt, f.EndedAt = parseTime(started), parseNullTime(ended)
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM check_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at <= ? AND success = 0`,
		monitorID, started, declared).Scan(&f.Attempts)
	return f, err
}

// LastLatency returns the duration of a monitor's newest successful check
// before the given time, or of its newest one when before is zero. ok is
// false when there is none; a check that measured nothing (a heartbeat)
// does not count. It walks idx_check_results_monitor_time backwards and
// stops at the first success.
func LastLatency(ctx context.Context, q querier, monitorID string, before time.Time) (d time.Duration, ok bool, err error) {
	query := `SELECT duration_ms FROM check_results WHERE monitor_id = ? AND success = 1 AND duration_ms > 0`
	args := []any{monitorID}
	if !before.IsZero() {
		query += ` AND checked_at < ?`
		args = append(args, formatTime(before))
	}
	var ms float64
	err = q.QueryRowContext(ctx, query+` ORDER BY checked_at DESC, id DESC LIMIT 1`, args...).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return msToDuration(ms), err == nil, err
}

// IncidentEnded reports whether an incident has ended. One that no longer
// exists has ended too.
func IncidentEnded(ctx context.Context, q querier, incidentID string) (bool, error) {
	var ended bool
	err := q.QueryRowContext(ctx, `SELECT ended_at IS NOT NULL FROM incidents WHERE id = ?`, incidentID).Scan(&ended)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return ended, err
}

// ClaimNotification marks an incident's DOWN or RECOVERY notification as
// handed to delivery at the given time (down_notified_at,
// recovery_notified_at) and reports whether this call did so. It reports
// false when it was claimed before, or the incident is gone: that
// notification must not be sent again, whatever happened to the first one.
// This is what keeps a notification single across a restart (docs/19).
func ClaimNotification(ctx context.Context, d *db.DB, incidentID string, kind incident.IntentKind, at time.Time) (bool, error) {
	column := "down_notified_at"
	if kind == incident.IntentRecovery {
		column = "recovery_notified_at"
	}
	var claimed bool
	err := db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `UPDATE incidents SET `+column+` = ? WHERE id = ? AND `+column+` IS NULL`,
			formatTime(at), incidentID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		claimed = n == 1
		return nil
	})
	return claimed, err
}

// AddNotificationEvent appends an entry about a notification to an
// incident's timeline. An incident that no longer exists gets none.
func AddNotificationEvent(ctx context.Context, d *db.DB, incidentID, eventType, message string, at time.Time) error {
	return db.Retry(ctx, func() error {
		_, err := d.Writer.ExecContext(ctx, `INSERT INTO incident_events (incident_id, event_type, message, created_at)
			SELECT id, ?, ?, ? FROM incidents WHERE id = ?`, eventType, nullStr(message), formatTime(at), incidentID)
		return err
	})
}

// Delivery statuses (notification_deliveries.status).
const (
	DeliverySent   = "sent"
	DeliveryFailed = "failed" // this attempt failed
	// DeliveryDropped: no attempt was made, because the incident had ended
	// before a retry was due.
	DeliveryDropped = "dropped"
)

// Channel health states (notification_channels.health_state, docs/11).
const (
	HealthUnknown = "unknown" // nothing was ever sent through it
	HealthHealthy = "healthy" // the latest attempt succeeded
	HealthWarning = "warning" // the latest attempt failed; the notification is still being retried
	HealthFailed  = "failed"  // a notification was given up; stays until the next success
)

// DeliveryAttempt is what became of one attempt to deliver a notification
// through a channel.
type DeliveryAttempt struct {
	IncidentID string // "" when the notification belongs to no incident
	ChannelID  string
	EventType  string // the kind of notification: down, recovery, …
	Attempt    int    // 1 for the first
	Status     string
	At         time.Time
	Error      string // why it failed or was dropped; never a secret
	// Final: no further attempt follows. The notification was delivered,
	// or it was given up.
	Final bool
	// TimelineMessage is added to the incident's timeline when the attempt
	// is final: notification_sent for a delivery, notification_failed
	// otherwise.
	TimelineMessage string
}

// RecordDelivery stores an attempt in one transaction: its row in
// notification_deliveries, the channel's health, and for a final attempt
// about an incident the entry in the incident's timeline. It returns
// ErrNotFound, having written nothing, when the channel is gone. An
// incident that was deleted meanwhile leaves only the channel's health.
func RecordDelivery(ctx context.Context, d *db.DB, a DeliveryAttempt) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var health string
		err = tx.QueryRowContext(ctx, `SELECT health_state FROM notification_channels WHERE id = ?`, a.ChannelID).Scan(&health)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		at := formatTime(a.At)
		switch a.Status {
		case DeliverySent:
			_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET health_state = ?, last_success_at = ? WHERE id = ?`,
				HealthHealthy, at, a.ChannelID)
		case DeliveryFailed:
			if a.Final {
				health = HealthFailed
			} else if health != HealthFailed {
				health = HealthWarning
			}
			_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET health_state = ?, last_failure_at = ?, last_error = ? WHERE id = ?`,
				health, at, nullStr(a.Error), a.ChannelID)
		}
		if err != nil {
			return err
		}

		incidentID := any(nil)
		if a.IncidentID != "" {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM incidents WHERE id = ?`, a.IncidentID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return tx.Commit()
			}
			if err != nil {
				return err
			}
			incidentID = a.IncidentID
		}
		var delivered any
		if a.Status == DeliverySent {
			delivered = at
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries
			(id, incident_id, channel_id, event_type, attempt, status, attempted_at, delivered_at, error_message)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ids.New(), incidentID, a.ChannelID, a.EventType, a.Attempt, a.Status, at, delivered, nullStr(a.Error)); err != nil {
			return err
		}
		if a.Final && a.IncidentID != "" {
			event := incident.EventNotificationFailed
			if a.Status == DeliverySent {
				event = incident.EventNotificationSent
			}
			if err := AddIncidentEvent(ctx, tx, a.IncidentID, event, a.TimelineMessage, a.At); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}
