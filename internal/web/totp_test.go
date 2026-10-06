package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
)

// totpCode is an independent RFC 6238 implementation (SHA-1, 6 digits,
// 30 s), so these tests do not trust the code under test to check itself.
func totpCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(secret, " ", ""))
	if err != nil {
		t.Fatalf("setup key %q: %v", secret, err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[19] & 0xf
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

var (
	totpSecretRe  = regexp.MustCompile(`<code class="totp-secret">([A-Z2-7 ]+)</code>`)
	totpPendingRe = regexp.MustCompile(`name="pending" value="([^"]+)"`)
	challengeRe   = regexp.MustCompile(`name="challenge" value="([^"]+)"`)
)

func match(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page lacks %s:\n%s", re, body)
	}
	return m[1]
}

type totpEnv struct {
	*appEnv
	cookie string
	sess   auth.Session
}

func newTOTPEnv(t *testing.T) *totpEnv {
	t.Helper()
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)
	cookie, sess := e.signIn(t, "u1")
	return &totpEnv{appEnv: e, cookie: cookie, sess: sess}
}

func (e *totpEnv) get(path string) *httptest.ResponseRecorder {
	return e.serve(withCookie(req("GET", path, nil), e.cookie))
}

func (e *totpEnv) post(path string, form url.Values) *httptest.ResponseRecorder {
	form.Set(CSRFFormField, e.csrf.token(e.sess.ID))
	return e.serve(withCookie(req("POST", path, form), e.cookie))
}

// follow adopts the rotated session a response issued.
func (e *totpEnv) follow(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := sessionCookie(rec)
	if c == nil || c.Value == e.cookie {
		t.Fatal("the session was not rotated")
	}
	sess, _, err := e.sessions.Lookup(context.Background(), c.Value, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.cookie, e.sess = c.Value, sess
}

// enable turns TOTP on through the pages and returns the setup key.
func (e *totpEnv) enable(t *testing.T) string {
	t.Helper()
	body := e.get("/settings/authentication/totp").Body.String()
	secret := match(t, totpSecretRe, body)
	rec := e.post("/settings/authentication/totp", url.Values{
		"pending": {match(t, totpPendingRe, body)}, "code": {totpCode(t, secret, time.Now())},
	})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Fatalf("enabling TOTP = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	e.follow(t, rec)
	e.forgetStep(t)
	return secret
}

// forgetStep clears replay protection, so a test can use the current code
// again without waiting 30 seconds. Replay itself is tested explicitly.
func (e *totpEnv) forgetStep(t *testing.T) {
	t.Helper()
	if _, err := e.db.Writer.Exec(`UPDATE users SET totp_last_step = NULL`); err != nil {
		t.Fatal(err)
	}
}

func (e *totpEnv) totpOn(t *testing.T) bool {
	t.Helper()
	var on bool
	if err := e.db.Reader.QueryRow(`SELECT totp_secret_enc IS NOT NULL FROM users WHERE id = 'u1'`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	return on
}

func (e *totpEnv) audited(t *testing.T, event string) int {
	t.Helper()
	var n int
	if err := e.db.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, event).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTOTPSetup(t *testing.T) {
	e := newTOTPEnv(t)
	other, _ := e.signIn(t, "u1")

	body := e.get("/settings/authentication").Body.String()
	if !strings.Contains(body, `<a href="/settings/authentication/totp">`) || !strings.Contains(body, "<strong>Off.</strong>") {
		t.Fatalf("settings page does not offer TOTP setup:\n%s", body)
	}

	rec := e.get("/settings/authentication/totp")
	body = rec.Body.String()
	secret, pending := match(t, totpSecretRe, body), match(t, totpPendingRe, body)
	raw := strings.ReplaceAll(secret, " ", "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("setup page = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, want := range []string{
		`<img class="totp-qr" src="data:image/png;base64,iVBOR`,
		`otpauth://totp/Sinjal:admin?`, `secret=` + raw, `autocomplete="one-time-code"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setup page lacks %s", want)
		}
	}
	if len(raw) != 32 {
		t.Errorf("setup key %q is not 160 bits of base32", secret)
	}
	if again := match(t, totpSecretRe, e.get("/settings/authentication/totp").Body.String()); again == secret {
		t.Error("reloading the page shows the same secret")
	}

	// A wrong code changes nothing and shows the same key again.
	rec = e.post("/settings/authentication/totp", url.Values{"pending": {pending}, "code": {"000000"}})
	if rec.Code != 422 || match(t, totpSecretRe, rec.Body.String()) != secret || !strings.Contains(rec.Body.String(), `role="alert"`) {
		t.Fatalf("wrong code = %d", rec.Code)
	}
	if e.totpOn(t) || sessionCookie(rec) != nil {
		t.Fatal("a wrong code enabled TOTP or rotated the session")
	}
	// So does a blob that is not ours; the page then starts over.
	rec = e.post("/settings/authentication/totp", url.Values{"pending": {"AAAA"}, "code": {totpCode(t, secret, time.Now())}})
	if rec.Code != 422 || match(t, totpSecretRe, rec.Body.String()) == secret || e.totpOn(t) {
		t.Fatalf("forged pending blob = %d", rec.Code)
	}

	old := e.cookie
	rec = e.post("/settings/authentication/totp", url.Values{"pending": {pending}, "code": {totpCode(t, secret, time.Now())}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Fatalf("right code = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	e.follow(t, rec)
	if !e.totpOn(t) || e.audited(t, "auth.totp_enabled") != 1 {
		t.Error("TOTP not enabled or not audited")
	}
	for name, cookie := range map[string]string{"previous": old, "other": other} {
		if _, _, err := e.sessions.Lookup(context.Background(), cookie, time.Now()); !errors.Is(err, auth.ErrNoSession) {
			t.Errorf("%s session survived the change: %v", name, err)
		}
	}
	if body := e.get("/settings/authentication").Body.String(); !strings.Contains(body, "<strong>On.</strong>") ||
		!strings.Contains(body, `action="/settings/authentication/totp/disable"`) {
		t.Errorf("settings page does not show TOTP as on:\n%s", body)
	}
	if rec := e.get("/settings/authentication/totp"); rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Errorf("setup page while enabled = %d", rec.Code)
	}
	if logs := e.logs.String(); strings.Contains(logs, raw) || strings.Contains(logs, secret) || strings.Contains(logs, pending) {
		t.Error("the TOTP secret reached the log")
	}
}

func TestTOTPManagementNeedsRecentAuthAndAdmin(t *testing.T) {
	e := newTOTPEnv(t)
	e.enable(t)
	old := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ?`, old); err != nil {
		t.Fatal(err)
	}

	if rec := e.get("/settings/authentication"); rec.Code != 200 {
		t.Errorf("the overview needs no recent auth, got %d", rec.Code)
	}
	if rec := e.get("/settings/authentication/totp"); rec.Code != 303 || rec.Header().Get("Location") != "/reauth?next=%2Fsettings%2Fauthentication%2Ftotp" {
		t.Errorf("stale GET setup = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{"/settings/authentication/totp", "/settings/authentication/totp/disable"} {
		if rec := e.post(path, url.Values{}); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
			t.Errorf("stale POST %s = %d %q, want 303 to /reauth", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if !e.totpOn(t) {
		t.Fatal("TOTP was disabled without recent authentication")
	}

	e.addUser(t, "v1", "viewer", "viewer", testPassword)
	viewer, _ := e.signIn(t, "v1")
	for _, path := range []string{"/settings/authentication", "/settings/authentication/totp"} {
		if rec := e.serve(withCookie(req("GET", path, nil), viewer)); rec.Code != 403 {
			t.Errorf("viewer GET %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestTOTPDisable(t *testing.T) {
	e := newTOTPEnv(t)
	e.enable(t)
	other, _ := e.signIn(t, "u1")

	rec := e.post("/settings/authentication/totp/disable", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Fatalf("disable = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	e.follow(t, rec)
	if e.totpOn(t) || e.audited(t, "auth.totp_disabled") != 1 {
		t.Error("TOTP still on or the change was not audited")
	}
	if _, _, err := e.sessions.Lookup(context.Background(), other, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("other session survived: %v", err)
	}
	// The password alone signs in again.
	if rec := e.serve(req("POST", "/login", loginForm("admin", testPassword, ""))); rec.Code != 303 || sessionCookie(rec) == nil {
		t.Errorf("login after disabling = %d", rec.Code)
	}
}

func TestLoginAsksForTheTOTPCode(t *testing.T) {
	e := newTOTPEnv(t)
	secret := e.enable(t)

	rec := e.serve(req("POST", "/login", loginForm("admin", testPassword, "/monitors")))
	body := rec.Body.String()
	if rec.Code != 200 || sessionCookie(rec) != nil {
		t.Fatalf("password step = %d, cookie %v; want the code form and no session", rec.Code, sessionCookie(rec))
	}
	challenge := match(t, challengeRe, body)
	for _, want := range []string{`action="/login/totp"`, `<input type="hidden" name="next" value="/monitors">`, `autocomplete="one-time-code"`} {
		if !strings.Contains(body, want) {
			t.Errorf("code form lacks %s", want)
		}
	}
	if e.audited(t, "auth.login_succeeded") != 0 {
		t.Error("the password alone was audited as a login")
	}
	step := func(challenge, code string) *httptest.ResponseRecorder {
		return e.serve(req("POST", "/login/totp", url.Values{"challenge": {challenge}, "code": {code}, "next": {"/monitors"}}))
	}

	rec = step(challenge, "000000")
	if rec.Code != 401 || sessionCookie(rec) != nil || !strings.Contains(rec.Body.String(), "Incorrect code.") ||
		match(t, challengeRe, rec.Body.String()) != challenge {
		t.Fatalf("wrong code = %d", rec.Code)
	}
	// A challenge that is not ours goes back to the password form.
	rec = step(challenge[:len(challenge)-2]+"AA", totpCode(t, secret, time.Now()))
	if rec.Code != 401 || sessionCookie(rec) != nil || !strings.Contains(rec.Body.String(), `action="/login"`) {
		t.Fatalf("forged challenge = %d", rec.Code)
	}

	code := totpCode(t, secret, time.Now())
	rec = step(challenge, code)
	if rec.Code != 303 || rec.Header().Get("Location") != "/monitors" {
		t.Fatalf("right code = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if _, u, err := e.sessions.Lookup(context.Background(), c.Value, time.Now()); err != nil || u.ID != "u1" {
		t.Fatalf("issued session: %+v %v", u, err)
	}
	// The same code does not work twice.
	if rec := step(challenge, code); rec.Code != 401 || sessionCookie(rec) != nil {
		t.Errorf("replayed code = %d, want 401", rec.Code)
	}
	if ok, failed := e.audited(t, "auth.login_succeeded"), e.audited(t, "auth.login_failed"); ok != 1 || failed != 2 {
		t.Errorf("audit: %d succeeded, %d failed; want 1 and 2", ok, failed)
	}
	if logs := e.logs.String(); strings.Contains(logs, challenge) || strings.Contains(logs, code) {
		t.Error("the challenge or a code reached the log")
	}
}

func TestLoginTOTPCodeRateLimit(t *testing.T) {
	e := newTOTPEnv(t)
	secret := e.enable(t)
	challenge := match(t, challengeRe, e.serve(req("POST", "/login", loginForm("admin", testPassword, ""))).Body.String())
	step := func(code, addr string) *httptest.ResponseRecorder {
		r := req("POST", "/login/totp", url.Values{"challenge": {challenge}, "code": {code}})
		r.RemoteAddr = addr
		return e.serve(r)
	}
	for i := range loginMaxFailures {
		// Spreading guesses over addresses does not help.
		if rec := step("000000", fmt.Sprintf("198.51.100.%d:1234", i+1)); rec.Code != 401 {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}
	rec := step(totpCode(t, secret, time.Now()), "192.0.2.1:1234")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || sessionCookie(rec) != nil {
		t.Fatalf("right code after %d failures = %d, want 429", loginMaxFailures, rec.Code)
	}
	if n := e.audited(t, "auth.login_failed"); n != loginMaxFailures {
		t.Errorf("audited failures = %d, want %d (blocked attempts are not checked)", n, loginMaxFailures)
	}
}

func TestReauthAsksForTheTOTPCode(t *testing.T) {
	e := newTOTPEnv(t)
	if body := e.get("/reauth").Body.String(); strings.Contains(body, `name="code"`) {
		t.Error("code field shown without TOTP")
	}
	secret := e.enable(t)
	if body := e.get("/reauth").Body.String(); !strings.Contains(body, `name="code"`) || !strings.Contains(body, "password and authentication code") {
		t.Errorf("re-auth form has no code field:\n%s", body)
	}

	rec := e.post("/reauth", url.Values{"password": {testPassword}})
	if rec.Code != 401 || sessionCookie(rec) != nil || !strings.Contains(rec.Body.String(), "Incorrect password or code.") {
		t.Fatalf("password without the code = %d", rec.Code)
	}
	rec = e.post("/reauth", url.Values{"password": {testPassword}, "code": {totpCode(t, secret, time.Now())}, "next": {"/settings/authentication"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" || sessionCookie(rec) == nil {
		t.Fatalf("password and code = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
