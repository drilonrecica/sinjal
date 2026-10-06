package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// TOTP per RFC 6238 with the parameters every authenticator app supports:
// HMAC-SHA1, 6 digits, 30-second steps, one step of clock drift either way.
const (
	totpDigits    = 6
	totpPeriod    = 30 // seconds
	totpSkew      = 1  // steps accepted before and after the current one
	totpSecretLen = 20 // bytes; the HMAC-SHA1 output size (RFC 4226 §4)
	totpIssuer    = "Sinjal"

	// TOTPEnrolmentWindow is how long a new secret may wait for its first code.
	TOTPEnrolmentWindow = 10 * time.Minute
	// TOTPChallengeWindow is how long a checked password waits for the code.
	TOTPChallengeWindow = 5 * time.Minute
)

var (
	// ErrTOTPRequired is returned by Login when the password is right and
	// the account also needs a code (LoginTOTP).
	ErrTOTPRequired = errors.New("auth: authentication code required")
	// ErrInvalidCode means the code does not match a new secret.
	ErrInvalidCode = errors.New("auth: invalid authentication code")
	// ErrTOTPExpired means a pending enrolment or login challenge is
	// expired, tampered with or belongs to someone else.
	ErrTOTPExpired = errors.New("auth: the pending TOTP step has expired")
	// ErrTOTPEnabled is returned by EnableTOTP when TOTP is already on.
	ErrTOTPEnabled = errors.New("auth: TOTP is already enabled")
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// hotp is the RFC 4226 one-time password for counter.
func hotp(secret []byte, counter uint64) string {
	mac := hmac.New(sha1.New, secret)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// matchTOTP returns the time step whose code equals code, looking at the
// current step and totpSkew steps around it. Spaces in the code are ignored.
// Every candidate is compared, in constant time.
func matchTOTP(secret []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	for _, c := range []byte(code) {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	var matched int64
	found := 0
	for step := totpStep(now) - totpSkew; step <= totpStep(now)+totpSkew; step++ {
		if step < 0 {
			continue
		}
		eq := subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(step))), []byte(code))
		matched = int64(subtle.ConstantTimeSelect(eq, int(step), int(matched)))
		found |= eq
	}
	return matched, found == 1
}

// totpURI is the otpauth:// URI authenticator apps read from the QR code.
func totpURI(login string, secret []byte) string {
	q := url.Values{
		"secret":    {totpEncoding.EncodeToString(secret)},
		"issuer":    {totpIssuer},
		"algorithm": {"SHA1"},
		"digits":    {fmt.Sprint(totpDigits)},
		"period":    {fmt.Sprint(totpPeriod)},
	}
	return (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + totpIssuer + ":" + login, RawQuery: q.Encode()}).String()
}

// sealTimed encrypts payload with an expiry for a round trip through the
// browser (a hidden form field). Nothing is stored on the server.
func (a *Authenticator) sealTimed(ctx vault.Context, payload []byte, expires time.Time) string {
	plain := binary.BigEndian.AppendUint64(make([]byte, 0, 8+len(payload)), uint64(expires.Unix()))
	return base64.RawURLEncoding.EncodeToString(a.key.Seal(ctx, append(plain, payload...)))
}

// openTimed reverses sealTimed. Every failure is ErrTOTPExpired.
func (a *Authenticator) openTimed(ctx vault.Context, blob string, now time.Time) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		return nil, ErrTOTPExpired
	}
	plain, err := a.key.Open(ctx, raw)
	if err != nil || len(plain) < 8 {
		return nil, ErrTOTPExpired
	}
	if now.Unix() >= int64(binary.BigEndian.Uint64(plain)) {
		return nil, ErrTOTPExpired
	}
	return plain[8:], nil
}

func totpSecretContext(userID string) vault.Context {
	return vault.Context{Table: "users", Column: "totp_secret_enc", RowID: userID}
}

func totpPendingContext(userID string) vault.Context {
	return vault.Context{Table: "users", Column: "totp_pending", RowID: userID}
}

var totpChallengeContext = vault.Context{Table: "login", Column: "totp_challenge"}

// TOTPEnrolment is a new secret waiting for its first code. Pending carries
// the secret, encrypted, through the confirmation form; the secret is stored
// only once EnableTOTP has seen a valid code.
type TOTPEnrolment struct {
	Secret  string // base32, for manual entry
	URI     string // otpauth:// URI, for the QR code
	Pending string
}

// NewTOTPEnrolment creates a secret for user. Showing it is a sensitive
// action: callers require recent re-authentication.
func (a *Authenticator) NewTOTPEnrolment(user User, now time.Time) (TOTPEnrolment, error) {
	secret := make([]byte, totpSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return TOTPEnrolment{}, err
	}
	pending := a.sealTimed(totpPendingContext(user.ID), secret, now.Add(TOTPEnrolmentWindow))
	return TOTPEnrolment{Secret: totpEncoding.EncodeToString(secret), URI: totpURI(user.Login, secret), Pending: pending}, nil
}

// TOTPEnrolmentFromPending rebuilds the enrolment a form was rendered with,
// to show the same secret again after a mistyped code.
func (a *Authenticator) TOTPEnrolmentFromPending(user User, pending string, now time.Time) (TOTPEnrolment, error) {
	secret, err := a.openTimed(totpPendingContext(user.ID), pending, now)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	return TOTPEnrolment{Secret: totpEncoding.EncodeToString(secret), URI: totpURI(user.Login, secret), Pending: pending}, nil
}

// TOTPEnabled reports whether userID has TOTP turned on.
func (a *Authenticator) TOTPEnabled(ctx context.Context, userID string) (bool, error) {
	var on bool
	err := a.db.Reader.QueryRowContext(ctx, `SELECT totp_secret_enc IS NOT NULL FROM users WHERE id = ?`, userID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// EnableTOTP turns TOTP on once code proves the authenticator app holds the
// pending secret. In one transaction it stores the encrypted secret, marks
// the code's time step as used, deletes the user's other sessions (keepID
// stays; the caller rotates it) and writes the audit event.
func (a *Authenticator) EnableTOTP(ctx context.Context, userID, pending, code, keepID, clientIP string, now time.Time) error {
	secret, err := a.openTimed(totpPendingContext(userID), pending, now)
	if err != nil {
		return err
	}
	step, ok := matchTOTP(secret, code, now)
	if !ok {
		return ErrInvalidCode
	}
	enc := a.key.Seal(totpSecretContext(userID), secret)
	return a.changeTOTP(ctx, userID, keepID, clientIP, "auth.totp_enabled", now,
		`UPDATE users SET totp_secret_enc = ?, totp_last_step = ?, updated_at = ? WHERE id = ? AND totp_secret_enc IS NULL`,
		enc, step, formatTime(now), userID)
}

// DisableTOTP turns TOTP off, deletes the user's other sessions and writes
// the audit event. Callers require recent re-authentication.
func (a *Authenticator) DisableTOTP(ctx context.Context, userID, keepID, clientIP string, now time.Time) error {
	return a.changeTOTP(ctx, userID, keepID, clientIP, "auth.totp_disabled", now,
		`UPDATE users SET totp_secret_enc = NULL, totp_last_step = NULL, updated_at = ? WHERE id = ? AND totp_secret_enc IS NOT NULL`,
		formatTime(now), userID)
}

// changeTOTP runs update and, when it changed the user's row, revokes the
// other sessions and audits event. An update that matches nothing means
// TOTP was already in the requested state.
func (a *Authenticator) changeTOTP(ctx context.Context, userID, keepID, clientIP, event string, now time.Time, update string, args ...any) error {
	return db.Retry(ctx, func() error {
		tx, err := a.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, update, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if event == "auth.totp_enabled" {
				return ErrTOTPEnabled
			}
			return nil // already off
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID); err != nil {
			return err
		}
		ev := audit{UserID: userID, Event: event, ObjectType: "user", ObjectID: userID, Metadata: map[string]string{"client_ip": clientIP}}
		if err := insertAudit(ctx, tx, ev, now); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// verifyTOTP checks code for userID and marks its time step as used, so the
// same code (or an older one) is never accepted twice. It reports false for
// users without TOTP.
func (a *Authenticator) verifyTOTP(ctx context.Context, userID, code string, now time.Time) (bool, error) {
	var enc []byte
	err := a.db.Reader.QueryRowContext(ctx, `SELECT totp_secret_enc FROM users WHERE id = ?`, userID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enc == nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := a.key.Open(totpSecretContext(userID), enc)
	if err != nil {
		a.log.Error("stored TOTP secret cannot be decrypted", "user_id", userID, "error", err)
		return false, nil
	}
	step, ok := matchTOTP(secret, code, now)
	if !ok {
		return false, nil
	}
	// Conditional on the stored step: of two requests with the same code,
	// one updates the row and the other does not.
	var used int64
	err = db.Retry(ctx, func() error {
		res, err := a.db.Writer.ExecContext(ctx, `UPDATE users SET totp_last_step = ?
			WHERE id = ? AND totp_secret_enc = ? AND (totp_last_step IS NULL OR totp_last_step < ?)`, step, userID, enc, step)
		if err != nil {
			return err
		}
		used, err = res.RowsAffected()
		return err
	})
	return used == 1, err
}

// TOTPChallenge returns the token that carries "the password was right"
// from the login form to the code form. It is encrypted, names the user and
// expires after TOTPChallengeWindow; it does not sign anyone in.
func (a *Authenticator) TOTPChallenge(userID string, now time.Time) string {
	return a.sealTimed(totpChallengeContext, []byte(userID), now.Add(TOTPChallengeWindow))
}

// OpenTOTPChallenge returns the user a challenge was issued for, or
// ErrTOTPExpired.
func (a *Authenticator) OpenTOTPChallenge(challenge string, now time.Time) (string, error) {
	userID, err := a.openTimed(totpChallengeContext, challenge, now)
	return string(userID), err
}

// LoginTOTP finishes a login that Login answered with ErrTOTPRequired. The
// caller has opened the challenge. Every failure is ErrInvalidCredentials;
// both outcomes are audited like a password login.
func (a *Authenticator) LoginTOTP(ctx context.Context, userID, code, clientIP string, now time.Time) (User, error) {
	c, found, err := a.lookup(ctx, `u.id = ?`, userID)
	if err != nil {
		return User{}, err
	}
	ok := false
	if found && !c.disabled && c.totp {
		if ok, err = a.verifyTOTP(ctx, userID, code, now); err != nil {
			return User{}, err
		}
	}
	ev := audit{Event: "auth.login_failed", Metadata: map[string]string{"client_ip": clientIP, "factor": "totp"}}
	if found {
		ev.UserID, ev.ObjectType, ev.ObjectID = userID, "user", userID
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
