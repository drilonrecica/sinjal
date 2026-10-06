package auth

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// ErrUserNotFound means no viewer has that id. Admins are never disabled
// through the web, so they are "not found" here too.
var ErrUserNotFound = errors.New("auth: no such viewer")

// Viewer is one row of the viewer list.
type Viewer struct {
	ID        string
	Login     string
	Disabled  bool
	CreatedAt time.Time
}

// CreateViewer adds a read-only account with a password chosen by the
// admin (no invitations, decision in docs/13). actorID is the admin, for
// the audit event. Inputs are validated (*InputError); a taken login is an
// *InputError on "login".
func CreateViewer(ctx context.Context, d *db.DB, actorID, login, password string, now time.Time) (string, error) {
	login, err := NormalizeLogin(login)
	if err != nil {
		return "", err
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}

	id := ids.New()
	ts := formatTime(now)
	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// Logins are unique case-insensitively in practice: two accounts
		// that differ only by case would be a phishing aid.
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE login = ? COLLATE NOCASE`, login).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return &InputError{"login", "That username is already taken."}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, login, role, password_hash, created_at, updated_at)
			VALUES (?, ?, 'viewer', ?, ?, ?)`, id, login, hash, ts, ts); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Event{UserID: actorID, Type: audit.ViewerCreated, ObjectType: "user", ObjectID: id}, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// SetViewerDisabled disables or re-enables a viewer. Disabling deletes all
// of the viewer's sessions in the same transaction (docs/13 "Sessions").
func SetViewerDisabled(ctx context.Context, d *db.DB, actorID, viewerID string, disabled bool, now time.Time) error {
	event := audit.ViewerEnabled
	if disabled {
		event = audit.ViewerDisabled
	}
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		res, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ? AND role = 'viewer'`,
			disabled, formatTime(now), viewerID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrUserNotFound
		}
		if disabled {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, viewerID); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, audit.Event{UserID: actorID, Type: event, ObjectType: "user", ObjectID: viewerID}, now); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ListViewers returns the viewer accounts, oldest first. It reads no
// credential column.
func ListViewers(ctx context.Context, q *sql.DB) ([]Viewer, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, login, disabled, created_at FROM users WHERE role = 'viewer' ORDER BY created_at, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Viewer
	for rows.Next() {
		var v Viewer
		var created string
		if err := rows.Scan(&v.ID, &v.Login, &v.Disabled, &created); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ChangePassword is SetPassword plus the audit event, in one transaction:
// the user's new password, their other sessions deleted (keepID survives)
// and `auth.password_changed`. Callers require recent re-authentication
// and rotate the kept session afterwards.
func ChangePassword(ctx context.Context, d *db.DB, userID, password, keepID, clientIP string, now time.Time) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := setPasswordTx(ctx, tx, userID, hash, keepID, now); err != nil {
			return err
		}
		ev := audit.Event{UserID: userID, Type: audit.PasswordChanged, ObjectType: "user", ObjectID: userID, Metadata: map[string]string{"client_ip": clientIP}}
		if err := audit.Write(ctx, tx, ev, now); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// SignOutOtherSessions deletes all of the user's sessions except keepID and
// audits it (`auth.sessions_revoked`, with the count), in one transaction.
// Callers require recent re-authentication. It returns how many were
// signed out.
func SignOutOtherSessions(ctx context.Context, d *db.DB, userID, keepID, clientIP string, now time.Time) (int64, error) {
	var n int64
	err := db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
		if err != nil {
			return err
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		meta := map[string]string{"client_ip": clientIP, "count": strconv.FormatInt(n, 10)}
		if err := audit.Write(ctx, tx, audit.Event{UserID: userID, Type: audit.SessionsRevoked, ObjectType: "user", ObjectID: userID, Metadata: meta}, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	return n, err
}
