// Package audit is the security and activity log (docs/13_AUTH_SECURITY.md
// "Audit log", docs/08_DATA_MODEL.md "Audit event"): who did what to which
// object, when, with minimal metadata. It is a small table, not an
// immutable compliance trail, and it never stores field-level diffs.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// Event types. Later milestones add theirs next to these (notification
// changes, backup restore).
const (
	SetupAdminCreated = "setup.admin_created"
	AdminResetCLI     = "admin_reset_cli"

	LoginSucceeded  = "auth.login_succeeded"
	LoginFailed     = "auth.login_failed"
	Reauthenticated = "auth.reauthenticated"
	ReauthFailed    = "auth.reauth_failed"
	PasswordChanged = "auth.password_changed"
	SessionsRevoked = "auth.sessions_revoked"
	TOTPEnabled     = "auth.totp_enabled"
	TOTPDisabled    = "auth.totp_disabled"
	PasskeyAdded    = "auth.passkey_added"
	PasskeyRemoved  = "auth.passkey_removed"

	ViewerCreated  = "user.viewer_created"
	ViewerDisabled = "user.viewer_disabled"
	ViewerEnabled  = "user.viewer_enabled"

	MonitorCreated = "monitor.created"
	MonitorDeleted = "monitor.deleted"
	MonitorPaused  = "monitor.paused"
	MonitorResumed = "monitor.resumed"

	MaintenanceCreated = "maintenance.created"
	MaintenanceUpdated = "maintenance.updated"
	MaintenanceDeleted = "maintenance.deleted"
)

// Event is one audit_events row to write. Empty strings are stored as NULL.
// UserID is the actor ("" when nobody is signed in, as for a failed login).
type Event struct {
	UserID     string
	Type       string
	ObjectType string
	ObjectID   string
	Metadata   map[string]string // never secrets, passwords or attempted logins
}

// Execer is a *sql.DB or *sql.Tx, so an event can join the transaction of
// the change it records.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Write inserts ev into q.
func Write(ctx context.Context, q Execer, ev Event, now time.Time) error {
	var meta any
	if len(ev.Metadata) > 0 {
		b, err := json.Marshal(ev.Metadata)
		if err != nil {
			return err
		}
		meta = string(b)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO audit_events (user_id, event_type, object_type, object_id, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(ev.UserID), ev.Type, nullIfEmpty(ev.ObjectType), nullIfEmpty(ev.ObjectID), meta, now.UTC().Format(time.RFC3339))
	return err
}

// Record writes ev on its own, retrying while the database is busy.
func Record(ctx context.Context, d *db.DB, ev Event, now time.Time) error {
	return db.Retry(ctx, func() error { return Write(ctx, d.Writer, ev, now) })
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Entry is one row of the list, newest first.
type Entry struct {
	ID         int64
	At         time.Time
	Actor      string // login of the actor; "" when none or the account was deleted
	Type       string
	ObjectType string
	ObjectID   string
	Metadata   map[string]string
}

// PageSize is how many entries one page of the list shows.
const PageSize = 50

// List returns up to limit entries with an id below before (0 = from the
// newest), newest first, and the cursor for the next page (0 when there is
// none). It pages by id, so a page costs the same however deep it is and
// new events never shift an older page.
func List(ctx context.Context, q *sql.DB, before int64, limit int) (entries []Entry, next int64, err error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id, a.created_at, COALESCE(u.login, ''), a.event_type,
			COALESCE(a.object_type, ''), COALESCE(a.object_id, ''), COALESCE(a.metadata_json, '')
		FROM audit_events a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.id < ? ORDER BY a.id DESC LIMIT ?`, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		var at, meta string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Type, &e.ObjectType, &e.ObjectID, &meta); err != nil {
			return nil, 0, err
		}
		e.At, _ = time.Parse(time.RFC3339, at)
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &e.Metadata) // a malformed row still lists, without metadata
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(entries) > limit {
		entries = entries[:limit]
		next = entries[limit-1].ID
	}
	return entries, next, nil
}
