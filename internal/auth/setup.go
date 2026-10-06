package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// Password policy (owner decision, M1-05): length only, no composition rules
// (NIST SP 800-63B). The maximum bounds the Argon2 input.
const (
	MinPasswordLen = 12   // characters
	MaxPasswordLen = 1024 // bytes
	MaxLoginLen    = 64   // characters
)

// ErrAdminExists is returned by CreateAdmin when an admin already exists at
// commit time.
var ErrAdminExists = errors.New("auth: an admin account already exists")

// InputError is a user-correctable validation failure on one form field.
type InputError struct {
	Field   string // "login" or "password"
	Message string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Message }

// NormalizeLogin trims the login and checks it: 1–64 characters, no
// whitespace or control characters.
func NormalizeLogin(login string) (string, error) {
	login = strings.TrimSpace(login)
	n := utf8.RuneCountInString(login)
	if n == 0 {
		return "", &InputError{"login", "Enter a username."}
	}
	if n > MaxLoginLen || !utf8.ValidString(login) {
		return "", &InputError{"login", fmt.Sprintf("Use at most %d characters.", MaxLoginLen)}
	}
	for _, r := range login {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", &InputError{"login", "Spaces and control characters are not allowed."}
		}
	}
	return login, nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return &InputError{"password", fmt.Sprintf("Use at least %d characters.", MinPasswordLen)}
	}
	if len(password) > MaxPasswordLen {
		return &InputError{"password", fmt.Sprintf("Use at most %d bytes.", MaxPasswordLen)}
	}
	return nil
}

// AdminExists reports whether any admin account exists. It always reads the
// database: auth state is never cached (a CLI reset must take effect).
func AdminExists(ctx context.Context, q *sql.DB) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin')`).Scan(&n)
	return n == 1, err
}

// SetupToken is the one-time secret that guards /setup while no admin exists
// (decision P0-10). It lives in memory only; a restart makes a new one.
type SetupToken struct {
	mu    sync.Mutex
	value string // "" once discarded
}

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSetupToken returns a fresh 128-bit token (26 lowercase base32 chars).
func NewSetupToken() *SetupToken {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms
	return &SetupToken{value: strings.ToLower(tokenEncoding.EncodeToString(b[:]))}
}

// Value returns the token for the startup log line, or "" once discarded.
func (t *SetupToken) Value() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.value
}

// Check compares candidate with the token in constant time. A discarded
// token matches nothing.
func (t *SetupToken) Check(candidate string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.value != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(t.value)) == 1
}

// Discard invalidates the token for good.
func (t *SetupToken) Discard() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.value = ""
}

// CreateAdmin creates the first admin account and its audit event in one
// transaction that succeeds only if no admin exists at commit time. Inputs
// are validated (*InputError); the password is hashed before the
// transaction so the single writer connection is not held for ~30 ms.
func CreateAdmin(ctx context.Context, d *db.DB, login, password string, now time.Time) (string, error) {
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
	ts := now.UTC().Format(time.RFC3339)
	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		res, err := tx.ExecContext(ctx, `INSERT INTO users (id, login, role, password_hash, created_at, updated_at)
			SELECT ?, ?, 'admin', ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin')`,
			id, login, hash, ts, ts)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrAdminExists
		}
		if err := audit.Write(ctx, tx, audit.Event{UserID: id, Type: audit.SetupAdminCreated, ObjectType: "user", ObjectID: id}, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
