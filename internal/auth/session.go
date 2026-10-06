package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// Session policy (decision P0-09, docs/13_AUTH_SECURITY.md "Sessions").
const (
	SessionLifetime  = 30 * 24 * time.Hour // absolute, never extended
	LastSeenInterval = 5 * time.Minute     // at most one last_seen_at write per interval
	tokenBytes       = 32
	maxUserAgent     = 256 // characters stored from the User-Agent header
)

// ErrNoSession means the token is unknown, malformed or expired, or its user
// is disabled. Callers treat the request as signed out.
var ErrNoSession = errors.New("auth: no valid session")

// Session is a stored login session. The token itself is never stored.
type Session struct {
	ID                string
	UserID            string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	LastSeenAt        time.Time
	ReauthenticatedAt time.Time // zero when never re-authenticated
}

// User is the account behind a session, with what request handling needs.
type User struct {
	ID      string
	Login   string
	Role    string // "admin" or "viewer"
	Theme   string // "" = instance default
	Density string
}

// Sessions stores sessions in the database. Lookups always read the
// database, so revocations (including by the CLI) take effect immediately.
type Sessions struct {
	db  *db.DB
	log *slog.Logger
}

// NewSessions returns the session store.
func NewSessions(d *db.DB, logger *slog.Logger) *Sessions {
	return &Sessions{db: d, log: logger}
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// hashToken decodes a cookie token and returns its SHA-256, or false when
// the token cannot be one Sinjal issued.
func hashToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}

// Create starts a session for userID. A fresh login counts as
// re-authentication. It returns the token for the cookie.
func (s *Sessions) Create(ctx context.Context, userID, userAgent, ip string, now time.Time) (string, Session, error) {
	var token string
	var sess Session
	err := db.Retry(ctx, func() error {
		tx, err := s.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if token, sess, err = insertSession(ctx, tx, userID, userAgent, ip, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	return token, sess, err
}

// Rotate replaces old with a new session for the same user in one
// transaction, after login-equivalent proof (re-authentication, password
// change). The old token stops working.
func (s *Sessions) Rotate(ctx context.Context, old Session, userAgent, ip string, now time.Time) (string, Session, error) {
	var token string
	var sess Session
	err := db.Retry(ctx, func() error {
		tx, err := s.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, old.ID, old.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSession
		}
		if token, sess, err = insertSession(ctx, tx, old.UserID, userAgent, ip, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	return token, sess, err
}

func insertSession(ctx context.Context, tx *sql.Tx, userID, userAgent, ip string, now time.Time) (string, Session, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, err
	}
	hash := sha256.Sum256(raw)
	now = now.UTC().Truncate(time.Second)
	sess := Session{
		ID: ids.New(), UserID: userID,
		CreatedAt: now, ExpiresAt: now.Add(SessionLifetime), LastSeenAt: now, ReauthenticatedAt: now,
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(id, token_hash, user_id, created_at, expires_at, last_seen_at, reauthenticated_at, user_agent, ip_hint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hash[:], userID, formatTime(now), formatTime(sess.ExpiresAt), formatTime(now), formatTime(now),
		nullIfEmpty(truncateRunes(userAgent, maxUserAgent)), nullIfEmpty(ip))
	if err != nil {
		return "", Session{}, fmt.Errorf("create session: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), sess, nil
}

// Lookup resolves a cookie token. Expired sessions and disabled users give
// ErrNoSession. last_seen_at is written at most once per LastSeenInterval,
// so most requests do no database write; a failed write is only logged.
func (s *Sessions) Lookup(ctx context.Context, token string, now time.Time) (Session, User, error) {
	hash, ok := hashToken(token)
	if !ok {
		return Session{}, User{}, ErrNoSession
	}
	var sess Session
	var u User
	var created, expires, lastSeen string
	var reauth, theme sql.NullString
	err := s.db.Reader.QueryRowContext(ctx, `SELECT s.id, s.user_id, s.created_at, s.expires_at, s.last_seen_at,
			s.reauthenticated_at, u.login, u.role, u.theme, u.density
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`,
		hash, formatTime(now)).
		Scan(&sess.ID, &sess.UserID, &created, &expires, &lastSeen, &reauth, &u.Login, &u.Role, &theme, &u.Density)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, User{}, err
	}
	u.ID, u.Theme = sess.UserID, theme.String
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	sess.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeen)
	if reauth.Valid {
		sess.ReauthenticatedAt, _ = time.Parse(time.RFC3339, reauth.String)
	}

	if now.Sub(sess.LastSeenAt) >= LastSeenInterval {
		// Conditional on the old value: concurrent requests write once.
		err := db.Retry(ctx, func() error {
			_, err := s.db.Writer.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at = ?`,
				formatTime(now), sess.ID, lastSeen)
			return err
		})
		if err != nil {
			s.log.Warn("session: last_seen_at update failed", "session_id", sess.ID, "error", err)
		} else {
			sess.LastSeenAt = now.UTC().Truncate(time.Second)
		}
	}
	return sess, u, nil
}

// Delete removes one session (logout).
func (s *Sessions) Delete(ctx context.Context, id string) error {
	return s.exec(ctx, nil, `DELETE FROM sessions WHERE id = ?`, id)
}

// DeleteOthers removes every session of userID except keepID: password,
// TOTP and passkey changes, "Sign out other sessions".
func (s *Sessions) DeleteOthers(ctx context.Context, userID, keepID string) (int64, error) {
	var n int64
	err := s.exec(ctx, &n, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	return n, err
}

// DeleteAllForUser removes every session of userID: user disabled, deleted
// or role changed, account reset.
func (s *Sessions) DeleteAllForUser(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := s.exec(ctx, &n, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return n, err
}

// DeleteExpired removes sessions whose absolute lifetime has ended.
func (s *Sessions) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.exec(ctx, &n, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(now))
	return n, err
}

func (s *Sessions) exec(ctx context.Context, affected *int64, q string, args ...any) error {
	return db.Retry(ctx, func() error {
		res, err := s.db.Writer.ExecContext(ctx, q, args...)
		if err == nil && affected != nil {
			*affected, err = res.RowsAffected()
		}
		return err
	})
}

// SetPassword stores a new password for userID and, in the same
// transaction, deletes all of the user's other sessions (keepID may be "" to
// delete all). The caller rotates the kept session afterwards.
func SetPassword(ctx context.Context, d *db.DB, userID, password, keepID string, now time.Time) error {
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
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
			hash, formatTime(now), userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("set password: user %s not found", userID)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
