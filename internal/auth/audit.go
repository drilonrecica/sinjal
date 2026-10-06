package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// execer is a *sql.DB or *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// audit is one audit_events row. Empty strings are stored as NULL.
type audit struct {
	UserID     string
	Event      string // "auth.login_succeeded", …
	ObjectType string
	ObjectID   string
	Metadata   map[string]string // never secrets or attempted logins
}

// insertAudit writes one audit event. It is the auth package's writer until
// M1-17 adds the shared one.
func insertAudit(ctx context.Context, q execer, a audit, now time.Time) error {
	var meta any
	if len(a.Metadata) > 0 {
		b, err := json.Marshal(a.Metadata)
		if err != nil {
			return err
		}
		meta = string(b)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO audit_events (user_id, event_type, object_type, object_id, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(a.UserID), a.Event, nullIfEmpty(a.ObjectType), nullIfEmpty(a.ObjectID), meta, formatTime(now))
	return err
}
