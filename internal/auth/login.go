package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// ErrInvalidCredentials is the only failure a login reports: unknown user,
// wrong password, disabled account and an account without a password look
// the same to the caller (no user enumeration).
var ErrInvalidCredentials = errors.New("auth: invalid username or password")

// verify is VerifyPassword; tests swap it to observe calls.
var verify = VerifyPassword

var (
	dummyOnce sync.Once
	dummyHash string
)

// dummy returns a valid hash of a random password. Failed lookups verify
// against it so they cost as much as a wrong password for a real account.
func dummy() string {
	dummyOnce.Do(func() {
		h, err := HashPassword(rand.Text())
		if err != nil {
			panic("auth: hashing the dummy password: " + err.Error())
		}
		dummyHash = h
	})
	return dummyHash
}

// Authenticator checks passwords for login and re-authentication and
// writes their audit events.
type Authenticator struct {
	db  *db.DB
	log *slog.Logger
}

// NewAuthenticator returns the password checker.
func NewAuthenticator(d *db.DB, logger *slog.Logger) *Authenticator {
	return &Authenticator{db: d, log: logger}
}

type credentials struct {
	user     User
	hash     string // "" when the account has no password
	disabled bool
}

// Login checks login and password. Every failure is ErrInvalidCredentials
// and costs one password verification, whether or not the account exists.
// Both outcomes are audited with the client IP; the attempted login string
// is never stored (people type passwords into it by mistake). A correct
// password with outdated hash parameters is rehashed.
func (a *Authenticator) Login(ctx context.Context, login, password, clientIP string, now time.Time) (User, error) {
	var c credentials
	found := false
	if norm, err := NormalizeLogin(login); err == nil {
		c, found, err = a.lookup(ctx, `u.login = ?`, norm)
		if err != nil {
			return User{}, err
		}
	}
	ok, err := a.check(ctx, c, found, password, now)
	if err != nil {
		return User{}, err
	}
	ev := audit{Event: "auth.login_failed", Metadata: map[string]string{"client_ip": clientIP}}
	if found {
		ev.UserID, ev.ObjectType, ev.ObjectID = c.user.ID, "user", c.user.ID
	}
	if ok {
		ev.Event = "auth.login_succeeded"
	}
	if err := a.audit(ctx, ev, now); err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, ErrInvalidCredentials
	}
	return c.user, nil
}

// ReauthWindow is how long a login or re-authentication allows sensitive
// actions (decision P0-09).
const ReauthWindow = 10 * time.Minute

// RecentlyAuthenticated reports whether s proved the password (or another
// factor) within ReauthWindow before now.
func (s Session) RecentlyAuthenticated(now time.Time) bool {
	return !s.ReauthenticatedAt.IsZero() && now.Sub(s.ReauthenticatedAt) < ReauthWindow
}

// Reauthenticate checks the signed-in user's password again before a
// sensitive action. Failures are ErrInvalidCredentials. Both outcomes are
// audited (auth.reauthenticated / auth.reauth_failed). TOTP (M1-13) and
// passkeys (M1-14) add their own checks next to this one.
func (a *Authenticator) Reauthenticate(ctx context.Context, userID, password, clientIP string, now time.Time) error {
	c, found, err := a.lookup(ctx, `u.id = ?`, userID)
	if err != nil {
		return err
	}
	ok, err := a.check(ctx, c, found, password, now)
	if err != nil {
		return err
	}
	ev := audit{Event: "auth.reauth_failed", Metadata: map[string]string{"client_ip": clientIP}}
	if found { // the account may have been deleted since the session was loaded
		ev.UserID, ev.ObjectType, ev.ObjectID = userID, "user", userID
	}
	if ok {
		ev.Event = "auth.reauthenticated"
	}
	if err := a.audit(ctx, ev, now); err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	return nil
}

// check verifies password against c (or the dummy hash) and rehashes on
// success when the parameters changed. Disabled accounts and accounts
// without a password never match.
func (a *Authenticator) check(ctx context.Context, c credentials, found bool, password string, now time.Time) (bool, error) {
	hash := c.hash
	usable := found && !c.disabled && hash != ""
	if !usable {
		hash = dummy()
	}
	if len(password) > MaxPasswordLen {
		return false, nil // nothing that long was ever accepted; skip the hash
	}
	ok, rehash, err := verify(password, hash)
	if err != nil {
		if errors.Is(err, ErrInvalidHash) {
			a.log.Error("stored password hash is malformed", "user_id", c.user.ID)
			return false, nil
		}
		return false, err
	}
	if !ok || !usable {
		return false, nil
	}
	if rehash {
		a.rehash(ctx, c.user.ID, password, hash, now)
	}
	return true, nil
}

// rehash stores a hash with the current parameters. It only replaces the
// hash it verified, so a concurrent password change wins. A failure is
// logged; the login itself has succeeded.
func (a *Authenticator) rehash(ctx context.Context, userID, password, old string, now time.Time) {
	h, err := HashPassword(password)
	if err == nil {
		err = db.Retry(ctx, func() error {
			_, err := a.db.Writer.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ?
				WHERE id = ? AND password_hash = ?`, h, formatTime(now), userID, old)
			return err
		})
	}
	if err != nil {
		a.log.Warn("password rehash failed", "user_id", userID, "error", err)
		return
	}
	a.log.Info("password rehashed with current parameters", "user_id", userID)
}

func (a *Authenticator) lookup(ctx context.Context, where string, arg string) (credentials, bool, error) {
	var c credentials
	var hash, theme sql.NullString
	err := a.db.Reader.QueryRowContext(ctx, `SELECT u.id, u.login, u.role, u.theme, u.density, u.password_hash, u.disabled
		FROM users u WHERE `+where, arg).
		Scan(&c.user.ID, &c.user.Login, &c.user.Role, &theme, &c.user.Density, &hash, &c.disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return credentials{}, false, nil
	}
	if err != nil {
		return credentials{}, false, err
	}
	c.user.Theme, c.hash = theme.String, hash.String
	return c, true, nil
}

func (a *Authenticator) audit(ctx context.Context, ev audit, now time.Time) error {
	return db.Retry(ctx, func() error { return insertAudit(ctx, a.db.Writer, ev, now) })
}
