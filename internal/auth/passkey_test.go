package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth/passkeytest"
)

const (
	testRPID   = "sinjal.example"
	testOrigin = "https://sinjal.example"
)

type passkeyEnv struct {
	loginEnv
	p    *Passkeys
	logs *bytes.Buffer
}

func newPasskeyEnv(t *testing.T) passkeyEnv {
	t.Helper()
	e := newLoginEnv(t)
	logs := &bytes.Buffer{}
	p := NewPasskeys(e.a.db, testOrigin, slog.New(slog.NewTextHandler(logs, nil)))
	if p.Unavailable() != "" {
		t.Fatal(p.Unavailable())
	}
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	return passkeyEnv{loginEnv: e, p: p, logs: logs}
}

func optionsJSON(t *testing.T, options any) []byte {
	t.Helper()
	b, err := OptionsJSON(options)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// register adds a's credential to userID.
func (e passkeyEnv) register(a *passkeytest.Authenticator, userID, label string) (Passkey, error) {
	e.t.Helper()
	ctx := context.Background()
	options, c, err := e.p.BeginRegistration(ctx, userID, label)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := a.Create(optionsJSON(e.t, options))
	if err != nil {
		e.t.Fatal(err)
	}
	return e.p.FinishRegistration(ctx, c, userID, bytes.NewReader(resp), "", "203.0.113.9", now)
}

func (e passkeyEnv) mustRegister(userID, label string) *passkeytest.Authenticator {
	e.t.Helper()
	a := passkeytest.New(testRPID, testOrigin)
	if _, err := e.register(a, userID, label); err != nil {
		e.t.Fatalf("registering a passkey: %v\n%s", err, e.logs)
	}
	return a
}

// login runs a sign-in ceremony with a.
func (e passkeyEnv) login(a *passkeytest.Authenticator, at time.Time) (User, error) {
	e.t.Helper()
	options, c, err := e.p.BeginLogin()
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := a.Get(optionsJSON(e.t, options))
	if err != nil {
		e.t.Fatal(err)
	}
	return e.p.FinishLogin(context.Background(), c, bytes.NewReader(resp), "203.0.113.9", at)
}

func TestPasskeyRelyingParty(t *testing.T) {
	for _, tc := range []struct{ baseURL, rpID, origin, reason string }{
		{"https://sinjal.example", "sinjal.example", "https://sinjal.example", ""},
		{"https://Sinjal.Example:8443/monitoring", "sinjal.example", "https://sinjal.example:8443", ""},
		{"http://localhost:8080", "localhost", "http://localhost:8080", ""},
		{"http://dev.localhost", "dev.localhost", "http://dev.localhost", ""},
		{"", "", "", "SINJAL_BASE_URL is not set"},
		{"http://sinjal.example", "", "", "is not https"},
		{"https://192.0.2.10", "", "", "is an IP address"},
		{"http://127.0.0.1:8080", "", "", "is an IP address"},
		{"https://[2001:db8::1]", "", "", "is an IP address"},
	} {
		rpID, origin, reason := passkeyRelyingParty(tc.baseURL)
		if rpID != tc.rpID || origin != tc.origin || !strings.Contains(reason, tc.reason) || (tc.reason == "") != (reason == "") {
			t.Errorf("%q → %q %q %q; want %q %q and reason containing %q", tc.baseURL, rpID, origin, reason, tc.rpID, tc.origin, tc.reason)
		}
	}
}

func TestPasskeysUnavailable(t *testing.T) {
	e := newLoginEnv(t)
	p := NewPasskeys(e.a.db, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if !strings.Contains(p.Unavailable(), "SINJAL_BASE_URL is not set") || p.Origin() != "" || p.Offered(ctx) {
		t.Fatalf("unavailable = %q, origin %q", p.Unavailable(), p.Origin())
	}
	if _, _, err := p.BeginLogin(); !errors.Is(err, ErrPasskeysUnavailable) {
		t.Errorf("BeginLogin: %v", err)
	}
	if _, _, err := p.BeginRegistration(ctx, "u1", ""); !errors.Is(err, ErrPasskeysUnavailable) {
		t.Errorf("BeginRegistration: %v", err)
	}
	if _, _, err := p.BeginReauth(ctx, "u1"); !errors.Is(err, ErrPasskeysUnavailable) {
		t.Errorf("BeginReauth: %v", err)
	}
	if _, err := p.FinishLogin(ctx, PasskeyCeremony{}, strings.NewReader("{}"), "", now); !errors.Is(err, ErrPasskeysUnavailable) {
		t.Errorf("FinishLogin: %v", err)
	}
}

func TestPasskeyRegistration(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	e.session("u1", "keep")
	e.session("u1", "other")

	options, c, err := e.p.BeginRegistration(ctx, "u1", "  Laptop  ")
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		PublicKey struct {
			RP                     struct{ ID, Name string }
			User                   struct{ Name string }
			Attestation            string
			AuthenticatorSelection struct {
				ResidentKey, UserVerification string
				RequireResidentKey            bool
			}
			ExcludeCredentials []struct{ ID string }
		}
	}
	raw := optionsJSON(t, options)
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	pk := o.PublicKey
	if pk.RP.ID != testRPID || pk.User.Name != "admin" || pk.Attestation != "none" || pk.AuthenticatorSelection.ResidentKey != "required" ||
		!pk.AuthenticatorSelection.RequireResidentKey || pk.AuthenticatorSelection.UserVerification != "required" || len(pk.ExcludeCredentials) != 0 {
		t.Errorf("creation options = %s", raw)
	}

	a := passkeytest.New(testRPID, testOrigin)
	a.BE = true
	resp, _ := a.Create(raw)
	k, err := e.p.FinishRegistration(ctx, c, "u1", bytes.NewReader(resp), "keep", "203.0.113.9", now)
	if err != nil {
		t.Fatalf("FinishRegistration: %v\n%s", err, e.logs)
	}
	if k.Label != "Laptop" || k.ID == "" || !k.CreatedAt.Equal(now) {
		t.Errorf("passkey = %+v", k)
	}
	var be bool
	var transports string
	e.a.db.Reader.QueryRow(`SELECT backup_eligible, transports_json FROM passkeys WHERE id = ?`, k.ID).Scan(&be, &transports)
	if !be || transports != `["internal"]` {
		t.Errorf("stored backup_eligible %v transports %q", be, transports)
	}
	if got := e.sessions("u1"); got != "keep" {
		t.Errorf("sessions after adding a passkey = %q, want only keep", got)
	}
	if got := e.audits(); len(got) != 1 || got[0].event != "auth.passkey_added" || got[0].userID != "u1" {
		t.Errorf("audit = %+v", got)
	}

	// The same response cannot be stored twice, and the same authenticator is excluded next time.
	if _, err := e.p.FinishRegistration(ctx, c, "u1", bytes.NewReader(resp), "keep", "203.0.113.9", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("registering the credential again: %v", err)
	}
	options, _, _ = e.p.BeginRegistration(ctx, "u1", "")
	json.Unmarshal(optionsJSON(t, options), &o)
	if len(o.PublicKey.ExcludeCredentials) != 1 {
		t.Errorf("the existing credential is not excluded: %+v", o.PublicKey.ExcludeCredentials)
	}

	// A second, unlabelled passkey.
	e.mustRegister("u1", "")
	list, err := e.p.List(ctx, "u1")
	if err != nil || len(list) != 2 || list[0].Label != "Laptop" || list[1].Label != "Passkey" || !list[0].LastUsedAt.IsZero() {
		t.Errorf("list = %+v, %v", list, err)
	}
	if !e.p.Offered(ctx) || !e.p.OfferedTo(ctx, "u1") || e.p.OfferedTo(ctx, "u2") {
		t.Error("Offered / OfferedTo do not reflect the stored passkeys")
	}
}

func TestPasskeyRegistrationRejects(t *testing.T) {
	e := newPasskeyEnv(t)
	e.user("u2", "other", "admin", "", false)
	ctx := context.Background()

	for name, change := range map[string]func(*passkeytest.Authenticator){
		"wrong origin":         func(a *passkeytest.Authenticator) { a.Origin = "https://evil.example" },
		"wrong relying party":  func(a *passkeytest.Authenticator) { a.RPID = "evil.example" },
		"no user verification": func(a *passkeytest.Authenticator) { a.UV = false },
		"backed up, not eligible": func(a *passkeytest.Authenticator) {
			a.BS = true
		},
	} {
		a := passkeytest.New(testRPID, testOrigin)
		change(a)
		if _, err := e.register(a, "u1", ""); !errors.Is(err, ErrPasskeyRejected) {
			t.Errorf("%s: %v, want ErrPasskeyRejected", name, err)
		}
	}

	// A response to another challenge, another user's ceremony, garbage.
	a := passkeytest.New(testRPID, testOrigin)
	options, c1, _ := e.p.BeginRegistration(ctx, "u1", "")
	_, c2, _ := e.p.BeginRegistration(ctx, "u1", "")
	resp, _ := a.Create(optionsJSON(t, options))
	if _, err := e.p.FinishRegistration(ctx, c2, "u1", bytes.NewReader(resp), "", "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("response to another challenge: %v", err)
	}
	if _, err := e.p.FinishRegistration(ctx, c1, "u2", bytes.NewReader(resp), "", "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("ceremony of another user: %v", err)
	}
	if _, err := e.p.FinishRegistration(ctx, c1, "u1", strings.NewReader(`{"id":`), "", "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("malformed response: %v", err)
	}
	_, loginCeremony, _ := e.p.BeginLogin()
	if _, err := e.p.FinishRegistration(ctx, loginCeremony, "u1", bytes.NewReader(resp), "", "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("sign-in ceremony finished as a registration: %v", err)
	}
	if n := count(t, e.a.db, `SELECT COUNT(*) FROM passkeys`); n != 0 {
		t.Fatalf("%d passkeys stored by rejected registrations", n)
	}

	for name, label := range map[string]string{"too long": strings.Repeat("x", MaxPasskeyLabelLen+1), "control character": "a\x00b"} {
		var in *InputError
		if _, _, err := e.p.BeginRegistration(ctx, "u1", label); !errors.As(err, &in) || in.Field != "label" {
			t.Errorf("%s label: %v, want an InputError", name, err)
		}
	}
	if _, _, err := e.p.BeginRegistration(ctx, "u1", strings.Repeat("ë", MaxPasskeyLabelLen)); err != nil {
		t.Errorf("a %d-character label is refused: %v", MaxPasskeyLabelLen, err)
	}
	if _, _, err := e.p.BeginRegistration(ctx, "gone", ""); err == nil {
		t.Error("registration begun for an unknown user")
	}
}

func TestPasskeyLimit(t *testing.T) {
	e := newPasskeyEnv(t)
	for i := range maxPasskeys {
		if _, err := e.a.db.Writer.Exec(`INSERT INTO passkeys (id, user_id, credential_id, public_key, created_at) VALUES (?, 'u1', ?, x'00', 'now')`,
			i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	var in *InputError
	if _, _, err := e.p.BeginRegistration(context.Background(), "u1", ""); !errors.As(err, &in) {
		t.Errorf("passkey %d: %v, want an InputError", maxPasskeys+1, err)
	}
}

func TestPasskeyLogin(t *testing.T) {
	e := newPasskeyEnv(t)
	e.user("u2", "second", "viewer", "", false)
	a1 := e.mustRegister("u1", "Laptop")
	a2 := e.mustRegister("u2", "Phone")
	before := len(e.audits())
	at := now.Add(time.Hour)

	options, _, err := e.p.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(optionsJSON(t, options))
	if !strings.Contains(raw, `"rpId":"sinjal.example"`) || !strings.Contains(raw, `"userVerification":"required"`) || strings.Contains(raw, "allowCredentials") {
		t.Errorf("request options = %s", raw)
	}

	u, err := e.login(a1, at)
	if err != nil || u.ID != "u1" || u.Login != "admin" || u.Role != "admin" {
		t.Fatalf("login with u1's passkey = %+v, %v\n%s", u, err, e.logs)
	}
	if u, err := e.login(a2, at); err != nil || u.ID != "u2" || u.Role != "viewer" {
		t.Fatalf("login with u2's passkey = %+v, %v", u, err)
	}
	list, _ := e.p.List(context.Background(), "u1")
	if len(list) != 1 || !list[0].LastUsedAt.Equal(at) {
		t.Errorf("last used = %+v, want %v", list, at)
	}
	got := e.audits()[before:]
	if len(got) != 2 || got[0].event != "auth.login_succeeded" || got[0].userID != "u1" || !strings.Contains(got[0].meta, `"factor":"passkey"`) {
		t.Errorf("audit = %+v", got)
	}
}

func TestPasskeyLoginRejects(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	a := e.mustRegister("u1", "")
	before := len(e.audits())

	for name, change := range map[string]func(*passkeytest.Authenticator){
		"wrong origin":            func(a *passkeytest.Authenticator) { a.Origin = "https://evil.example" },
		"wrong relying party":     func(a *passkeytest.Authenticator) { a.RPID = "evil.example" },
		"no user verification":    func(a *passkeytest.Authenticator) { a.UV = false },
		"backup eligibility flip": func(a *passkeytest.Authenticator) { a.BE = true },
		"another account's handle": func(a *passkeytest.Authenticator) {
			a.UserHandle = []byte("u2")
		},
		"unknown account":    func(a *passkeytest.Authenticator) { a.UserHandle = []byte("gone") },
		"unknown credential": func(a *passkeytest.Authenticator) { a.CredentialID = []byte("0123456789abcdef") },
	} {
		clone := *a
		change(&clone)
		if _, err := e.login(&clone, now); !errors.Is(err, ErrPasskeyRejected) {
			t.Errorf("%s: %v, want ErrPasskeyRejected", name, err)
		}
	}
	e.user("u2", "second", "admin", "", false)

	// A response to another challenge; a ceremony of the wrong kind; garbage.
	options, _, _ := e.p.BeginLogin()
	_, other, _ := e.p.BeginLogin()
	resp, _ := a.Get(optionsJSON(t, options))
	if _, err := e.p.FinishLogin(ctx, other, bytes.NewReader(resp), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("response to another challenge: %v", err)
	}
	reauthOptions, reauth, _ := e.p.BeginReauth(ctx, "u1")
	reauthResp, _ := a.Get(optionsJSON(t, reauthOptions))
	if _, err := e.p.FinishLogin(ctx, reauth, bytes.NewReader(reauthResp), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("re-auth ceremony finished as a sign-in: %v", err)
	}
	if _, err := e.p.FinishLogin(ctx, other, strings.NewReader("nonsense"), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("malformed response: %v", err)
	}

	failures := e.audits()[before:]
	if len(failures) != 10 {
		t.Fatalf("%d audit rows for 10 failures: %+v", len(failures), failures)
	}
	for _, r := range failures {
		if r.event != "auth.login_failed" || !strings.Contains(r.meta, `"factor":"passkey"`) {
			t.Errorf("audit row = %+v", r)
		}
	}

	// Disabled accounts cannot sign in; a removed passkey stops working.
	if _, err := e.a.db.Writer.Exec(`UPDATE users SET disabled = 1 WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login(a, now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("disabled account: %v", err)
	}
	e.a.db.Writer.Exec(`UPDATE users SET disabled = 0 WHERE id = 'u1'`)
	if _, err := e.login(a, now); err != nil {
		t.Fatalf("re-enabled account: %v", err)
	}
	e.a.db.Writer.Exec(`DELETE FROM passkeys`)
	if _, err := e.login(a, now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("removed passkey: %v", err)
	}
}

func TestPasskeySignCount(t *testing.T) {
	e := newPasskeyEnv(t)
	a := e.mustRegister("u1", "")
	stored := func() int {
		return count(t, e.a.db, `SELECT sign_count FROM passkeys`)
	}

	// Counters that stay at zero (synced passkeys) are fine.
	for range 2 {
		if _, err := e.login(a, now); err != nil {
			t.Fatalf("zero counter: %v", err)
		}
	}
	a.Counter = 5
	if _, err := e.login(a, now); err != nil || stored() != 5 {
		t.Fatalf("counter 5: %v, stored %d", err, stored())
	}
	for _, c := range []uint32{5, 4, 0} {
		a.Counter = c
		if _, err := e.login(a, now); !errors.Is(err, ErrPasskeyRejected) {
			t.Errorf("counter %d after 5: %v, want ErrPasskeyRejected", c, err)
		}
	}
	if stored() != 5 || !strings.Contains(e.logs.String(), "may be cloned") {
		t.Errorf("stored count %d; clone warning logged: %v", stored(), strings.Contains(e.logs.String(), "may be cloned"))
	}
	a.Counter = 6
	if _, err := e.login(a, now); err != nil || stored() != 6 {
		t.Errorf("counter 6: %v, stored %d", err, stored())
	}
}

func TestPasskeyReauth(t *testing.T) {
	e := newPasskeyEnv(t)
	e.user("u2", "second", "admin", "", false)
	e.user("u3", "third", "admin", "", false)
	a1 := e.mustRegister("u1", "")
	a2 := e.mustRegister("u2", "")
	ctx := context.Background()
	before := len(e.audits())

	if _, _, err := e.p.BeginReauth(ctx, "u3"); !errors.Is(err, ErrNoPasskeys) {
		t.Errorf("account without passkeys: %v, want ErrNoPasskeys", err)
	}
	begin := func() ([]byte, PasskeyCeremony) {
		t.Helper()
		options, c, err := e.p.BeginReauth(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		return optionsJSON(t, options), c
	}
	raw, c := begin()
	if !strings.Contains(string(raw), "allowCredentials") || !strings.Contains(string(raw), `"userVerification":"required"`) {
		t.Errorf("request options = %s", raw)
	}

	// Someone else's passkey, the wrong user, a sign-in ceremony.
	resp2, _ := a2.Get(raw)
	if err := e.p.FinishReauth(ctx, c, "u1", bytes.NewReader(resp2), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("another account's passkey: %v", err)
	}
	resp1, _ := a1.Get(raw)
	if err := e.p.FinishReauth(ctx, c, "u2", bytes.NewReader(resp1), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("ceremony of another user: %v", err)
	}
	loginOptions, loginCeremony, _ := e.p.BeginLogin()
	loginResp, _ := a1.Get(optionsJSON(t, loginOptions))
	if err := e.p.FinishReauth(ctx, loginCeremony, "u1", bytes.NewReader(loginResp), "", now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("sign-in ceremony finished as a re-auth: %v", err)
	}

	if err := e.p.FinishReauth(ctx, c, "u1", bytes.NewReader(resp1), "203.0.113.9", now); err != nil {
		t.Fatalf("own passkey: %v\n%s", err, e.logs)
	}
	got := e.audits()[before:]
	if len(got) != 4 || got[0].event != "auth.reauth_failed" || got[3].event != "auth.reauthenticated" || got[3].userID != "u1" ||
		!strings.Contains(got[3].meta, `"factor":"passkey"`) {
		t.Errorf("audit = %+v", got)
	}
}

func TestPasskeyDelete(t *testing.T) {
	e := newPasskeyEnv(t)
	e.user("u2", "second", "admin", "", false)
	a := e.mustRegister("u1", "Laptop")
	e.mustRegister("u1", "Phone")
	ctx := context.Background()
	list, _ := e.p.List(ctx, "u1")
	e.session("u1", "keep")
	e.session("u1", "other")

	for name, tc := range map[string]struct{ user, id string }{"another user's": {"u2", list[0].ID}, "unknown": {"u1", "nope"}} {
		if err := e.p.Delete(ctx, tc.user, tc.id, "keep", "", now); !errors.Is(err, ErrPasskeyNotFound) {
			t.Errorf("%s passkey: %v, want ErrPasskeyNotFound", name, err)
		}
	}
	if got := e.sessions("u1"); got != "keep,other" {
		t.Fatalf("a refused delete revoked sessions: %q", got)
	}

	if err := e.p.Delete(ctx, "u1", list[0].ID, "keep", "203.0.113.9", now); err != nil {
		t.Fatal(err)
	}
	if left, _ := e.p.List(ctx, "u1"); len(left) != 1 || left[0].Label != "Phone" {
		t.Errorf("left = %+v", left)
	}
	if got := e.sessions("u1"); got != "keep" {
		t.Errorf("sessions = %q, want only keep", got)
	}
	if got := e.audits(); got[len(got)-1].event != "auth.passkey_removed" {
		t.Errorf("audit = %+v", got[len(got)-1])
	}
	if _, err := e.login(a, now); !errors.Is(err, ErrPasskeyRejected) {
		t.Errorf("removed passkey still signs in: %v", err)
	}
}
