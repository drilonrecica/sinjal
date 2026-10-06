package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
)

// Reset errors; the CLI prints them as they are.
var (
	ErrNoAdmin        = errors.New("no admin account exists")
	ErrAdminNotFound  = errors.New("no admin account with that login")
	ErrAdminAmbiguous = errors.New("more than one admin exists; pass --login <login>")
)

// resetPasswordBytes of randomness encode to a 24-character password.
const resetPasswordBytes = 18

// ResetAdmin is the account-recovery path (decision P0-11): it gives an
// admin a new random password and returns it for the operator to read once.
// In one transaction it also clears TOTP, deletes all of the admin's
// sessions, optionally deletes the admin's passkeys, and writes the
// admin_reset_cli audit event. login may be "" when exactly one admin
// exists. The server needs no restart: it never caches authentication state.
func ResetAdmin(ctx context.Context, d *db.DB, login string, removePasskeys bool, now time.Time) (password string, err error) {
	if login = strings.TrimSpace(login); login != "" {
		if login, err = NormalizeLogin(login); err != nil {
			return "", err
		}
	}
	raw := make([]byte, resetPasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	password = base64.RawURLEncoding.EncodeToString(raw)
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}

	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		id, err := resetTarget(ctx, tx, login)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, totp_secret_enc = NULL, totp_last_step = NULL, updated_at = ? WHERE id = ?`,
			hash, formatTime(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
		meta := map[string]string{"passkeys_removed": "false"}
		if removePasskeys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id = ?`, id); err != nil {
				return err
			}
			meta["passkeys_removed"] = "true"
		}
		if err := audit.Write(ctx, tx, audit.Event{UserID: id, Type: audit.AdminResetCLI, ObjectType: "user", ObjectID: id, Metadata: meta}, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	return password, nil
}

// resetTarget picks the admin to reset inside the transaction, so the
// choice and the write cannot be separated by another writer.
func resetTarget(ctx context.Context, tx *sql.Tx, login string) (string, error) {
	if login != "" {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE login = ? AND role = 'admin'`, login).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrAdminNotFound
		}
		return id, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE role = 'admin' LIMIT 2`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", ErrNoAdmin
	case 1:
		return found[0], nil
	}
	return "", ErrAdminAmbiguous
}
