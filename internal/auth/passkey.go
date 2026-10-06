package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

const (
	// MaxPasskeyLabelLen is the longest label, in characters.
	MaxPasskeyLabelLen = 64
	// maxPasskeys bounds the credentials of one user.
	maxPasskeys         = 20
	defaultPasskeyLabel = "Passkey"
)

var (
	// ErrPasskeysUnavailable means SINJAL_BASE_URL does not allow WebAuthn;
	// Passkeys.Unavailable has the reason.
	ErrPasskeysUnavailable = errors.New("auth: passkeys are not available")
	// ErrPasskeyRejected is the only failure a ceremony reports: a response
	// that does not verify, an unknown credential, a disabled account and a
	// ceremony finished at the wrong place look the same to the caller.
	ErrPasskeyRejected = errors.New("auth: the passkey response was not accepted")
	// ErrNoPasskeys means the user has no passkey to authenticate with.
	ErrNoPasskeys = errors.New("auth: the account has no passkeys")
	// ErrPasskeyNotFound means the passkey does not exist or belongs to
	// another user.
	ErrPasskeyNotFound = errors.New("auth: passkey not found")
)

// Passkey is one registered WebAuthn credential, as listed in Settings.
type Passkey struct {
	ID         string
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero when never used
}

// Ceremony kinds. A ceremony can only be finished as what it was begun as.
const (
	ceremonyRegister = "register"
	ceremonyLogin    = "login"
	ceremonyReauth   = "reauth"
)

// PasskeyCeremony is the server-side state between the begin and finish
// steps of one WebAuthn ceremony: the challenge and what it was issued for.
// The caller keeps it for a few minutes and hands it back exactly once.
type PasskeyCeremony struct {
	kind    string
	userID  string // "" for a sign-in, where the passkey names the user
	label   string
	session webauthn.SessionData
}

// Passkeys runs WebAuthn ceremonies and stores credentials. Users and
// credentials are read from the database on every ceremony, never cached.
type Passkeys struct {
	db     *db.DB
	log    *slog.Logger
	wa     *webauthn.WebAuthn // nil when unavailable
	origin string
	reason string
}

// NewPasskeys configures WebAuthn from SINJAL_BASE_URL: the relying party ID
// is its host and the only accepted origin is its scheme and host. When the
// URL cannot work with WebAuthn, passkeys are unavailable and Unavailable
// says why; nothing else is affected.
func NewPasskeys(d *db.DB, baseURL string, logger *slog.Logger) *Passkeys {
	p := &Passkeys{db: d, log: logger}
	rpID, origin, reason := passkeyRelyingParty(baseURL)
	if reason != "" {
		p.reason = reason
		return p
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Sinjal",
		RPOrigins:     []string{origin},
		// No attestation and no metadata service: nothing is fetched.
		AttestationPreference: protocol.PreferNoAttestation,
		// A passkey signs in on its own, so it must be discoverable (no
		// username is typed) and must verify the user (PIN or biometric).
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		p.reason = fmt.Sprintf("SINJAL_BASE_URL (%s) cannot be used for passkeys: %v.", baseURL, err)
		return p
	}
	p.wa, p.origin = wa, origin
	return p
}

// passkeyRelyingParty derives the RP ID and origin from the base URL, or
// explains why WebAuthn cannot work with it.
func passkeyRelyingParty(baseURL string) (rpID, origin, reason string) {
	if baseURL == "" {
		return "", "", "SINJAL_BASE_URL is not set. A passkey is tied to the address Sinjal is opened at, so set it to that address (for example https://status.example.com) and restart."
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "", "", fmt.Sprintf("SINJAL_BASE_URL (%s) is not a usable address.", baseURL)
	}
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return "", "", fmt.Sprintf("SINJAL_BASE_URL (%s) is an IP address. Passkeys need a host name.", baseURL)
	}
	local := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if u.Scheme != "https" && !local {
		return "", "", fmt.Sprintf("SINJAL_BASE_URL (%s) is not https. Browsers allow passkeys only over https (or on http://localhost).", baseURL)
	}
	return host, u.Scheme + "://" + strings.ToLower(u.Host), ""
}

// Unavailable returns why passkeys cannot be used, or "" when they can.
func (p *Passkeys) Unavailable() string { return p.reason }

// Origin is the only origin ceremonies are accepted from, "" when
// unavailable.
func (p *Passkeys) Origin() string { return p.origin }

// passkeyUser adapts an account and its credentials to webauthn.User. The
// user handle is the account ID: random, not personal data, 32 bytes.
type passkeyUser struct {
	user  User
	creds []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return []byte(u.user.ID) }
func (u *passkeyUser) WebAuthnName() string                       { return u.user.Login }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.user.Login }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (u *passkeyUser) descriptors() []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, len(u.creds))
	for i := range u.creds {
		out[i] = u.creds[i].Descriptor()
	}
	return out
}

var errNoPasskeyUser = errors.New("unknown or disabled account")

// loadUser reads an enabled account and its credentials.
func (p *Passkeys) loadUser(ctx context.Context, userID string) (*passkeyUser, error) {
	u := &passkeyUser{}
	var theme sql.NullString
	err := p.db.Reader.QueryRowContext(ctx, `SELECT id, login, role, theme, density FROM users WHERE id = ? AND disabled = 0`, userID).
		Scan(&u.user.ID, &u.user.Login, &u.user.Role, &theme, &u.user.Density)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoPasskeyUser
	}
	if err != nil {
		return nil, err
	}
	u.user.Theme = theme.String

	rows, err := p.db.Reader.QueryContext(ctx, `SELECT credential_id, public_key, sign_count, transports_json, backup_eligible
		FROM passkeys WHERE user_id = ? ORDER BY created_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c webauthn.Credential
		var transports sql.NullString
		if err := rows.Scan(&c.ID, &c.PublicKey, &c.Authenticator.SignCount, &transports, &c.Flags.BackupEligible); err != nil {
			return nil, err
		}
		if transports.Valid {
			if err := json.Unmarshal([]byte(transports.String), &c.Transport); err != nil {
				return nil, fmt.Errorf("passkey transports: %w", err)
			}
		}
		u.creds = append(u.creds, c)
	}
	return u, rows.Err()
}

// List returns the user's passkeys, oldest first (insertion order within
// the same second).
func (p *Passkeys) List(ctx context.Context, userID string) ([]Passkey, error) {
	rows, err := p.db.Reader.QueryContext(ctx, `SELECT id, COALESCE(label, ''), created_at, COALESCE(last_used_at, '')
		FROM passkeys WHERE user_id = ? ORDER BY created_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var k Passkey
		var created, used string
		if err := rows.Scan(&k.ID, &k.Label, &created, &used); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, created)
		k.LastUsedAt, _ = time.Parse(time.RFC3339, used)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Offered reports whether the sign-in page should offer a passkey: they are
// available and at least one is registered. A read error only hides the
// button.
func (p *Passkeys) Offered(ctx context.Context) bool {
	return p.any(ctx, `SELECT EXISTS (SELECT 1 FROM passkeys)`)
}

// OfferedTo reports whether userID can re-authenticate with a passkey.
func (p *Passkeys) OfferedTo(ctx context.Context, userID string) bool {
	return p.any(ctx, `SELECT EXISTS (SELECT 1 FROM passkeys WHERE user_id = ?)`, userID)
}

func (p *Passkeys) any(ctx context.Context, query string, args ...any) bool {
	if p.wa == nil {
		return false
	}
	var exists bool
	if err := p.db.Reader.QueryRowContext(ctx, query, args...).Scan(&exists); err != nil {
		p.log.Error("passkeys: checking for credentials failed", "error", err)
		return false
	}
	return exists
}

// normalizePasskeyLabel trims the label; empty becomes "Passkey".
func normalizePasskeyLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return defaultPasskeyLabel, nil
	}
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxPasskeyLabelLen {
		return "", &InputError{"label", fmt.Sprintf("Use at most %d characters.", MaxPasskeyLabelLen)}
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", &InputError{"label", "Control characters are not allowed."}
		}
	}
	return label, nil
}

// BeginRegistration starts adding a passkey for user. options is the JSON
// value for navigator.credentials.create. Callers require recent
// re-authentication.
func (p *Passkeys) BeginRegistration(ctx context.Context, userID, label string) (options any, c PasskeyCeremony, err error) {
	if p.wa == nil {
		return nil, c, ErrPasskeysUnavailable
	}
	if label, err = normalizePasskeyLabel(label); err != nil {
		return nil, c, err
	}
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return nil, c, err
	}
	if len(u.creds) >= maxPasskeys {
		return nil, c, &InputError{"label", fmt.Sprintf("An account can have at most %d passkeys. Remove one first.", maxPasskeys)}
	}
	// Excluding the existing credentials stops the same authenticator from
	// being registered twice.
	creation, session, err := p.wa.BeginRegistration(u, webauthn.WithExclusions(u.descriptors()))
	if err != nil {
		return nil, c, err
	}
	return creation, PasskeyCeremony{kind: ceremonyRegister, userID: userID, label: label, session: *session}, nil
}

// FinishRegistration verifies the browser's response and stores the new
// credential. In the same transaction it deletes the user's other sessions
// (keepID stays; the caller rotates it) and writes the audit event.
func (p *Passkeys) FinishRegistration(ctx context.Context, c PasskeyCeremony, userID string, response io.Reader, keepID, clientIP string, now time.Time) (Passkey, error) {
	if p.wa == nil {
		return Passkey{}, ErrPasskeysUnavailable
	}
	if c.kind != ceremonyRegister || c.userID != userID {
		return Passkey{}, p.rejected(c.kind, errors.New("ceremony was begun for something else"))
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(response)
	if err != nil {
		return Passkey{}, p.rejected(c.kind, err)
	}
	u, err := p.loadUser(ctx, userID)
	if errors.Is(err, errNoPasskeyUser) {
		return Passkey{}, p.rejected(c.kind, err)
	}
	if err != nil {
		return Passkey{}, err
	}
	cred, err := p.wa.CreateCredential(u, c.session, parsed)
	if err != nil {
		return Passkey{}, p.rejected(c.kind, err)
	}
	var transports any
	if len(cred.Transport) > 0 {
		b, err := json.Marshal(cred.Transport)
		if err != nil {
			return Passkey{}, err
		}
		transports = string(b)
	}

	k := Passkey{ID: ids.New(), Label: c.label, CreatedAt: now.UTC().Truncate(time.Second)}
	err = db.Retry(ctx, func() error {
		tx, err := p.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// The exclusion list is advisory; the UNIQUE index is the rule.
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM passkeys WHERE credential_id = ?)`, cred.ID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrPasskeyRejected
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO passkeys
			(id, user_id, credential_id, public_key, sign_count, transports_json, backup_eligible, label, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			k.ID, userID, cred.ID, cred.PublicKey, cred.Authenticator.SignCount, transports, cred.Flags.BackupEligible, k.Label, formatTime(now)); err != nil {
			return err
		}
		return passkeyChanged(ctx, tx, userID, keepID, k.ID, "auth.passkey_added", clientIP, now)
	})
	if errors.Is(err, ErrPasskeyRejected) {
		return Passkey{}, p.rejected(c.kind, errors.New("credential is already registered"))
	}
	if err != nil {
		return Passkey{}, err
	}
	return k, nil
}

// passkeyChanged revokes the user's other sessions and audits a credential
// change, inside the transaction that made it.
func passkeyChanged(ctx context.Context, tx *sql.Tx, userID, keepID, passkeyID, event, clientIP string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID); err != nil {
		return err
	}
	ev := audit{UserID: userID, Event: event, ObjectType: "passkey", ObjectID: passkeyID, Metadata: map[string]string{"client_ip": clientIP}}
	if err := insertAudit(ctx, tx, ev, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete revokes one of the user's passkeys, deletes the user's other
// sessions and writes the audit event. Callers require recent
// re-authentication.
func (p *Passkeys) Delete(ctx context.Context, userID, passkeyID, keepID, clientIP string, now time.Time) error {
	return db.Retry(ctx, func() error {
		tx, err := p.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, passkeyID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrPasskeyNotFound
		}
		return passkeyChanged(ctx, tx, userID, keepID, passkeyID, "auth.passkey_removed", clientIP, now)
	})
}

// BeginLogin starts a sign-in with a discoverable credential: no user is
// named, the passkey the browser offers identifies the account. options is
// the JSON value for navigator.credentials.get.
func (p *Passkeys) BeginLogin() (options any, c PasskeyCeremony, err error) {
	if p.wa == nil {
		return nil, c, ErrPasskeysUnavailable
	}
	assertion, session, err := p.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, c, err
	}
	return assertion, PasskeyCeremony{kind: ceremonyLogin, session: *session}, nil
}

// FinishLogin verifies a sign-in assertion and returns the account it
// belongs to. Every failure is ErrPasskeyRejected. Both outcomes are audited
// like a password login, with "factor":"passkey".
func (p *Passkeys) FinishLogin(ctx context.Context, c PasskeyCeremony, response io.Reader, clientIP string, now time.Time) (User, error) {
	if p.wa == nil {
		return User{}, ErrPasskeysUnavailable
	}
	u, err := p.assert(ctx, c, ceremonyLogin, "", response, now)
	if err := p.auditAssertion(ctx, u, err, "auth.login_succeeded", "auth.login_failed", clientIP, now); err != nil {
		return User{}, err
	}
	if err != nil {
		return User{}, err
	}
	return u.user, nil
}

// BeginReauth starts a re-authentication with one of userID's passkeys.
func (p *Passkeys) BeginReauth(ctx context.Context, userID string) (options any, c PasskeyCeremony, err error) {
	if p.wa == nil {
		return nil, c, ErrPasskeysUnavailable
	}
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return nil, c, err
	}
	if len(u.creds) == 0 {
		return nil, c, ErrNoPasskeys
	}
	assertion, session, err := p.wa.BeginLogin(u)
	if err != nil {
		return nil, c, err
	}
	return assertion, PasskeyCeremony{kind: ceremonyReauth, userID: userID, session: *session}, nil
}

// FinishReauth verifies a re-authentication assertion for userID. Failures
// are ErrPasskeyRejected. Both outcomes are audited (auth.reauthenticated /
// auth.reauth_failed).
func (p *Passkeys) FinishReauth(ctx context.Context, c PasskeyCeremony, userID string, response io.Reader, clientIP string, now time.Time) error {
	if p.wa == nil {
		return ErrPasskeysUnavailable
	}
	u, err := p.assert(ctx, c, ceremonyReauth, userID, response, now)
	if err := p.auditAssertion(ctx, u, err, "auth.reauthenticated", "auth.reauth_failed", clientIP, now); err != nil {
		return err
	}
	return err
}

// assert verifies an assertion for a ceremony of the wanted kind (and user,
// for re-authentication) and records the use of the credential. It returns
// the account when it could be identified, also next to ErrPasskeyRejected,
// so the failure can be audited against it.
func (p *Passkeys) assert(ctx context.Context, c PasskeyCeremony, kind, userID string, response io.Reader, now time.Time) (*passkeyUser, error) {
	if c.kind != kind || c.userID != userID {
		return nil, p.rejected(kind, errors.New("ceremony was begun for something else"))
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(response)
	if err != nil {
		return nil, p.rejected(kind, err)
	}

	var u *passkeyUser
	var cred *webauthn.Credential
	if kind == ceremonyLogin {
		var lookupErr error
		_, cred, err = p.wa.ValidatePasskeyLogin(func(_, userHandle []byte) (webauthn.User, error) {
			u, lookupErr = p.loadUser(ctx, string(userHandle))
			return u, lookupErr
		}, c.session, parsed)
		if lookupErr != nil && !errors.Is(lookupErr, errNoPasskeyUser) {
			return nil, lookupErr // a database error, not a bad response
		}
	} else {
		if u, err = p.loadUser(ctx, userID); errors.Is(err, errNoPasskeyUser) {
			return nil, p.rejected(kind, err)
		} else if err != nil {
			return nil, err
		}
		cred, err = p.wa.ValidateLogin(u, c.session, parsed)
	}
	if err != nil {
		return u, p.rejected(kind, err)
	}
	// A counter that did not move forward means two copies of the
	// credential exist (WebAuthn §6.1.1). Counters that stay at zero, as
	// synced passkeys report, are not a signal.
	if cred.Authenticator.CloneWarning {
		p.log.Warn("passkey sign count went backwards; the credential may be cloned", "user_id", u.user.ID)
		return u, p.rejected(kind, errors.New("sign count did not increase"))
	}
	err = db.Retry(ctx, func() error {
		_, err := p.db.Writer.ExecContext(ctx, `UPDATE passkeys SET sign_count = ?, last_used_at = ? WHERE credential_id = ?`,
			cred.Authenticator.SignCount, formatTime(now), cred.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// auditAssertion writes the audit event for an assertion's outcome. Errors
// other than ErrPasskeyRejected are not an outcome and are not audited.
func (p *Passkeys) auditAssertion(ctx context.Context, u *passkeyUser, outcome error, ok, failed, clientIP string, now time.Time) error {
	ev := audit{Event: ok, Metadata: map[string]string{"client_ip": clientIP, "factor": "passkey"}}
	switch {
	case errors.Is(outcome, ErrPasskeyRejected):
		ev.Event = failed
	case outcome != nil:
		return nil
	}
	if u != nil {
		ev.UserID, ev.ObjectType, ev.ObjectID = u.user.ID, "user", u.user.ID
	}
	return db.Retry(ctx, func() error { return insertAudit(ctx, p.db.Writer, ev, now) })
}

// rejected logs why a response was refused and returns the one error
// callers see. The detail comes from the WebAuthn library and holds no
// secret: challenges are single-use and keys are public.
func (p *Passkeys) rejected(kind string, err error) error {
	reason := err.Error()
	var pe *protocol.Error
	if errors.As(err, &pe) {
		reason = strings.TrimSpace(pe.Details + " " + pe.DevInfo)
	}
	p.log.Info("passkey response rejected", "ceremony", kind, "reason", reason)
	return ErrPasskeyRejected
}

// OptionsJSON encodes ceremony options for the browser.
func OptionsJSON(options any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(options)
	return buf.Bytes(), err
}
