package auth

import (
	"context"
	"database/sql"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

var rfcSecret = []byte("12345678901234567890")

// RFC 4226 Appendix D: HOTP values for counters 0–9.
func TestHOTPVectors(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, w := range want {
		if got := hotp(rfcSecret, uint64(counter)); got != w {
			t.Errorf("hotp(%d) = %s, want %s", counter, got, w)
		}
	}
}

// RFC 6238 Appendix B (SHA-1). The RFC lists 8 digits; 6-digit codes are
// their last 6 digits.
func TestTOTPVectors(t *testing.T) {
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		at := time.Unix(unix, 0)
		if got := hotp(rfcSecret, uint64(totpStep(at))); got != want {
			t.Errorf("code at %d = %s, want %s", unix, got, want)
		}
		if step, ok := matchTOTP(rfcSecret, want, at); !ok || step != totpStep(at) {
			t.Errorf("matchTOTP at %d = %d %v", unix, step, ok)
		}
	}
}

func TestMatchTOTPWindow(t *testing.T) {
	step := totpStep(now)
	for offset, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		code := hotp(rfcSecret, uint64(step+offset))
		got, ok := matchTOTP(rfcSecret, code, now)
		if ok != want || (ok && got != step+offset) {
			t.Errorf("offset %d: step %d ok %v, want ok %v", offset, got, ok, want)
		}
	}
	code := hotp(rfcSecret, uint64(step))
	if _, ok := matchTOTP(rfcSecret, code[:3]+" "+code[3:], now); !ok {
		t.Error("a code typed with a space is refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", code + "0", "-" + code[1:], "１２３４５６"} {
		if _, ok := matchTOTP(rfcSecret, bad, now); ok {
			t.Errorf("malformed code %q accepted", bad)
		}
	}
}

func TestTOTPURI(t *testing.T) {
	u, err := url.Parse(totpURI("ops admin", rfcSecret))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Sinjal:ops admin" {
		t.Errorf("uri = %s", u)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(q.Get("secret"))
	if err != nil || string(secret) != string(rfcSecret) {
		t.Errorf("secret = %q (%v)", q.Get("secret"), err)
	}
	if q.Get("issuer") != "Sinjal" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("parameters = %v", q)
	}
}

// enrol turns TOTP on for userID and returns the secret.
func (e loginEnv) enrol(userID, login string, at time.Time) []byte {
	e.t.Helper()
	en, err := e.a.NewTOTPEnrolment(User{ID: userID, Login: login}, at)
	if err != nil {
		e.t.Fatal(err)
	}
	secret, err := totpEncoding.DecodeString(en.Secret)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.a.EnableTOTP(context.Background(), userID, en.Pending, code(secret, at), "", "203.0.113.9", at); err != nil {
		e.t.Fatal(err)
	}
	return secret
}

func code(secret []byte, at time.Time) string { return hotp(secret, uint64(totpStep(at))) }

func (e loginEnv) session(userID, id string) {
	e.t.Helper()
	if _, err := e.a.db.Writer.Exec(`INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at)
		VALUES (?, ?, ?, 'now', 'later', 'now')`, id, []byte(id), userID); err != nil {
		e.t.Fatal(err)
	}
}

func (e loginEnv) sessions(userID string) string {
	e.t.Helper()
	var ids sql.NullString
	if err := e.a.db.Reader.QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM sessions WHERE user_id = ? ORDER BY id)`, userID).Scan(&ids); err != nil {
		e.t.Fatal(err)
	}
	return ids.String
}

func TestEnableTOTP(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	e.user("u2", "other", "admin", e.hash(testPassword), false)
	e.session("u1", "keep")
	e.session("u1", "other")
	e.session("u2", "u2s")
	ctx := context.Background()

	en, err := e.a.NewTOTPEnrolment(User{ID: "u1", Login: "admin"}, now)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := totpEncoding.DecodeString(en.Secret)
	if len(secret) != 20 || !strings.Contains(en.URI, "secret="+en.Secret) {
		t.Fatalf("enrolment = %+v", en)
	}
	if again, _ := e.a.NewTOTPEnrolment(User{ID: "u1", Login: "admin"}, now); again.Secret == en.Secret {
		t.Error("two enrolments share a secret")
	}
	// The pending blob re-opens to the same secret, for re-rendering the page.
	if same, err := e.a.TOTPEnrolmentFromPending(User{ID: "u1", Login: "admin"}, en.Pending, now); err != nil || same.Secret != en.Secret || same.Pending != en.Pending {
		t.Errorf("re-opened enrolment = %+v, %v", same, err)
	}

	good := code(secret, now)
	tampered := en.Pending[:len(en.Pending)-2] + "AA"
	for name, tc := range map[string]struct {
		uid, pending, code string
		at                 time.Time
		want               error
	}{
		"wrong code":     {"u1", en.Pending, "000000", now, ErrInvalidCode},
		"tampered blob":  {"u1", tampered, good, now, ErrTOTPExpired},
		"not base64":     {"u1", "!!", good, now, ErrTOTPExpired},
		"another user's": {"u2", en.Pending, good, now, ErrTOTPExpired},
		"expired blob":   {"u1", en.Pending, code(secret, now.Add(TOTPEnrolmentWindow)), now.Add(TOTPEnrolmentWindow), ErrTOTPExpired},
	} {
		if err := e.a.EnableTOTP(ctx, tc.uid, tc.pending, tc.code, "keep", "203.0.113.9", tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if on, _ := e.a.TOTPEnabled(ctx, "u1"); on {
		t.Fatal("a failed attempt enabled TOTP")
	}

	if err := e.a.EnableTOTP(ctx, "u1", en.Pending, good, "keep", "203.0.113.9", now); err != nil {
		t.Fatal(err)
	}
	if on, err := e.a.TOTPEnabled(ctx, "u1"); !on || err != nil {
		t.Errorf("TOTPEnabled = %v, %v", on, err)
	}
	if got := e.sessions("u1"); got != "keep" {
		t.Errorf("u1 sessions = %q, want only keep", got)
	}
	if got := e.sessions("u2"); got != "u2s" {
		t.Errorf("u2 sessions = %q", got)
	}
	// Stored encrypted, bound to the row.
	var enc []byte
	e.a.db.Reader.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = 'u1'`).Scan(&enc)
	if len(enc) == 0 || strings.Contains(string(enc), string(secret)) {
		t.Errorf("stored secret is not an envelope: %x", enc)
	}
	// The enrolment code counts as used.
	if ok, err := e.a.verifyTOTP(ctx, "u1", good, now); ok || err != nil {
		t.Errorf("enrolment code reused: ok %v err %v", ok, err)
	}
	if err := e.a.EnableTOTP(ctx, "u1", en.Pending, good, "keep", "203.0.113.9", now); !errors.Is(err, ErrTOTPEnabled) {
		t.Errorf("enabling twice: %v, want ErrTOTPEnabled", err)
	}
	got := e.audits()
	if len(got) != 1 || got[0].event != "auth.totp_enabled" || got[0].userID != "u1" || !strings.Contains(got[0].meta, "203.0.113.9") {
		t.Errorf("audit = %+v", got)
	}
}

func TestVerifyTOTPRefusesReplay(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	secret := e.enrol("u1", "admin", now)
	ctx := context.Background()

	next := now.Add(30 * time.Second)
	later := now.Add(60 * time.Second)
	check := func(name, c string, at time.Time, want bool) {
		t.Helper()
		if ok, err := e.a.verifyTOTP(ctx, "u1", c, at); ok != want || err != nil {
			t.Errorf("%s: ok %v err %v, want %v", name, ok, err, want)
		}
	}
	check("wrong code", "000000", next, false)
	check("later step, inside the window", code(secret, later), next, true)
	check("same code again", code(secret, later), next, false)
	check("older step after a newer one", code(secret, next), next, false)
	check("next unused step", code(secret, later.Add(30*time.Second)), later, true)
	check("a used step, later on", code(secret, later), later, false)

	if ok, _ := e.a.verifyTOTP(ctx, "gone", code(secret, later), later); ok {
		t.Error("code accepted for an unknown user")
	}
}

func TestDisableTOTP(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	secret := e.enrol("u1", "admin", now)
	e.session("u1", "keep")
	e.session("u1", "other")
	ctx := context.Background()

	if err := e.a.DisableTOTP(ctx, "u1", "keep", "203.0.113.9", now); err != nil {
		t.Fatal(err)
	}
	if on, _ := e.a.TOTPEnabled(ctx, "u1"); on {
		t.Error("still enabled")
	}
	var enc, step sql.NullString
	e.a.db.Reader.QueryRow(`SELECT totp_secret_enc, totp_last_step FROM users WHERE id = 'u1'`).Scan(&enc, &step)
	if enc.Valid || step.Valid {
		t.Errorf("columns not cleared: %v %v", enc, step)
	}
	if got := e.sessions("u1"); got != "keep" {
		t.Errorf("sessions = %q, want only keep", got)
	}
	if ok, _ := e.a.verifyTOTP(ctx, "u1", code(secret, now.Add(time.Minute)), now.Add(time.Minute)); ok {
		t.Error("code accepted after disabling")
	}
	if got := e.audits(); got[len(got)-1].event != "auth.totp_disabled" {
		t.Errorf("audit = %+v", got)
	}
	// A new enrolment starts over, including the replay counter.
	e.enrol("u1", "admin", now.Add(-time.Hour))
}

func TestLoginWithTOTP(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	secret := e.enrol("u1", "admin", now)
	ctx := context.Background()
	at := now.Add(time.Minute)
	before := len(e.audits())

	if _, err := e.a.Login(ctx, "admin", "wrong password!", "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	u, err := e.a.Login(ctx, "admin", testPassword, "203.0.113.9", at)
	if !errors.Is(err, ErrTOTPRequired) || u.ID != "u1" {
		t.Fatalf("right password = %+v, %v; want ErrTOTPRequired", u, err)
	}
	if got := e.audits()[before:]; len(got) != 1 || got[0].event != "auth.login_failed" {
		t.Errorf("a password alone was audited as a login: %+v", got)
	}

	ch := e.a.TOTPChallenge("u1", at)
	if uid, err := e.a.OpenTOTPChallenge(ch, at.Add(TOTPChallengeWindow-time.Second)); uid != "u1" || err != nil {
		t.Errorf("challenge = %q, %v", uid, err)
	}
	for name, bad := range map[string]string{"tampered": ch[:len(ch)-2] + "AA", "empty": "", "enrolment blob": mustPending(t, e)} {
		if _, err := e.a.OpenTOTPChallenge(bad, at); !errors.Is(err, ErrTOTPExpired) {
			t.Errorf("%s challenge: %v, want ErrTOTPExpired", name, err)
		}
	}
	if _, err := e.a.OpenTOTPChallenge(ch, at.Add(TOTPChallengeWindow)); !errors.Is(err, ErrTOTPExpired) {
		t.Errorf("expired challenge: %v", err)
	}

	if _, err := e.a.LoginTOTP(ctx, "u1", "000000", "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong code: %v", err)
	}
	u, err = e.a.LoginTOTP(ctx, "u1", code(secret, at), "203.0.113.9", at)
	if err != nil || u.ID != "u1" || u.Login != "admin" || u.Role != "admin" {
		t.Fatalf("right code = %+v, %v", u, err)
	}
	if _, err := e.a.LoginTOTP(ctx, "u1", code(secret, at), "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("replayed code: %v", err)
	}
	got := e.audits()[before+1:]
	if len(got) != 3 || got[0].event != "auth.login_failed" || got[1].event != "auth.login_succeeded" || got[2].event != "auth.login_failed" ||
		!strings.Contains(got[1].meta, `"factor":"totp"`) || got[1].userID != "u1" {
		t.Errorf("audit = %+v", got)
	}

	// Accounts that cannot finish the second step.
	e.user("u2", "plain", "admin", e.hash(testPassword), false)
	if _, err := e.a.LoginTOTP(ctx, "u2", "000000", "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("account without TOTP: %v", err)
	}
	if _, err := e.a.db.Writer.Exec(`UPDATE users SET disabled = 1 WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Minute)
	if _, err := e.a.LoginTOTP(ctx, "u1", code(secret, later), "203.0.113.9", later); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled account: %v", err)
	}
}

func mustPending(t *testing.T, e loginEnv) string {
	t.Helper()
	en, err := e.a.NewTOTPEnrolment(User{ID: "u1", Login: "admin"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return en.Pending
}

func TestReauthenticateWithTOTP(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	secret := e.enrol("u1", "admin", now)
	ctx := context.Background()
	at := now.Add(time.Minute)

	for name, tc := range map[string]struct{ pw, code string }{
		"no code":        {testPassword, ""},
		"wrong code":     {testPassword, "000000"},
		"wrong password": {"wrong password!", code(secret, at)},
	} {
		if err := e.a.Reauthenticate(ctx, "u1", tc.pw, tc.code, "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v, want ErrInvalidCredentials", name, err)
		}
	}
	// The wrong-password attempt must not have used up the code.
	if err := e.a.Reauthenticate(ctx, "u1", testPassword, code(secret, at), "203.0.113.9", at); err != nil {
		t.Fatalf("password and code: %v", err)
	}
	if err := e.a.Reauthenticate(ctx, "u1", testPassword, code(secret, at), "203.0.113.9", at); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("replayed code: %v", err)
	}
}
