package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	byteorder "encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth/passkeytest"
	"github.com/drilonrecica/sinjal/internal/db"
)

// M1-19: the auth surface driven over HTTP against the real binary. The
// handler tests under internal/web cover the edge cases; these prove the
// shipped route table and middleware chain enforce the same rules.

// client is one signed-in browser: its session cookie and CSRF token.
type client struct {
	t     *testing.T
	s     *server
	token string
	csrf  string
}

// signIn logs in with a password and loads the CSRF token for the session.
func signIn(t *testing.T, s *server, user, password string) *client {
	t.Helper()
	token, code := login(t, s, user, password)
	if token == "" || code != 303 {
		t.Fatalf("login %s: status %d, token issued %v", user, code, token != "")
	}
	return &client{t: t, s: s, token: token, csrf: csrfFromPage(t, s.base+"/monitors", token)}
}

func (c *client) do(req *http.Request) (*http.Response, string) {
	c.t.Helper()
	if c.token != "" {
		req.AddCookie(&http.Cookie{Name: "sinjal_session", Value: c.token})
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (c *client) get(path string) (*http.Response, string) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.s.base+path, nil)
	return c.do(req)
}

// post submits a same-origin form carrying the client's CSRF token; hdr
// adds or overrides request headers (an empty value removes one).
func (c *client) post(path string, form url.Values, hdr ...string) (*http.Response, string) {
	c.t.Helper()
	form = maps.Clone(form) // callers reuse forms across session rotations
	if form == nil {
		form = url.Values{}
	}
	if _, set := form["_csrf"]; !set {
		form.Set("_csrf", c.csrf)
	}
	req, _ := http.NewRequest("POST", c.s.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	return c.do(req)
}

// postJSON sends a passkey ceremony step; ceremony is the begin cookie.
func (c *client) postJSON(path string, body []byte, ceremony string) (*http.Response, string) {
	c.t.Helper()
	req, _ := http.NewRequest("POST", c.s.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if ceremony != "" {
		req.AddCookie(&http.Cookie{Name: "sinjal_passkey", Value: ceremony})
	}
	return c.do(req)
}

// adopt takes over a rotated session cookie and its new CSRF token.
func (c *client) adopt(resp *http.Response) {
	c.t.Helper()
	tok := cookie(resp, "sinjal_session")
	if tok == "" {
		c.t.Fatalf("response %d did not rotate the session", resp.StatusCode)
	}
	c.token, c.csrf = tok, csrfFromPage(c.t, c.s.base+"/monitors", tok)
}

// signedIn reports whether the session still opens an app page.
func (c *client) signedIn() bool {
	c.t.Helper()
	resp, _ := c.get("/monitors")
	return resp.StatusCode == 200
}

func cookie(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// openDB opens the running server's database; busy_timeout covers the
// server's own writes.
func openDB(t *testing.T, dataDir string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(dataDir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func count(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Reader.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ageSessions moves a timestamp column of every session ago into the past.
func ageSessions(t *testing.T, d *db.DB, column string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	if _, err := d.Writer.Exec(`UPDATE sessions SET `+column+` = ?`, at); err != nil {
		t.Fatal(err)
	}
}

// notLogged fails for every secret that reached the server's output.
func notLogged(t *testing.T, s *server, secrets ...string) {
	t.Helper()
	logs := s.logs.String()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(logs, secret) {
			t.Errorf("the server log contains a secret: %q", secret)
		}
	}
}

func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test builds and runs the binary")
	}
}

// TestLoginRateLimit: after 10 failures for one login from one address the
// next attempt is refused before the password is checked, even when it is
// right, and the blocked attempt is not audited.
func TestLoginRateLimit(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	s := start(t, dataDir)
	createAdmin(t, s)

	for i := range 10 {
		if tok, code := login(t, s, "admin", fmt.Sprintf("wrong password %d", i)); tok != "" || code != 401 {
			t.Fatalf("failure %d: status %d, token issued %v", i+1, code, tok != "")
		}
	}
	resp, err := noRedirect.PostForm(s.base+"/login", url.Values{"login": {"Admin"}, "password": {adminPassword}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || cookie(resp, "sinjal_session") != "" {
		t.Fatalf("11th attempt = %d, Retry-After %q, session issued %v; want 429 and no session",
			resp.StatusCode, resp.Header.Get("Retry-After"), cookie(resp, "sinjal_session") != "")
	}
	d := openDB(t, dataDir)
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.login_failed'`); n != 10 {
		t.Errorf("auth.login_failed events = %d, want 10", n)
	}
	notLogged(t, s, adminPassword, "wrong password 0")
}

// TestCSRFRejection: a signed-in state change is refused without this
// session's token or from another site, and the session survives each try.
func TestCSRFRejection(t *testing.T) {
	skipShort(t)
	s := start(t, filepath.Join(t.TempDir(), "data"))
	createAdmin(t, s)
	a := signIn(t, s, "admin", adminPassword)
	b := signIn(t, s, "admin", adminPassword)

	cases := []struct {
		name string
		form url.Values
		hdr  []string
	}{
		{"no token", url.Values{"_csrf": {""}}, nil},
		{"wrong token", url.Values{"_csrf": {"not-a-token"}}, nil},
		{"another session's token", url.Values{"_csrf": {b.csrf}}, nil},
		{"cross-site fetch", nil, []string{"Sec-Fetch-Site", "cross-site"}},
		{"foreign Origin", nil, []string{"Sec-Fetch-Site", "", "Origin", "https://evil.example"}},
	}
	for _, tc := range cases {
		if resp, _ := a.post("/logout", tc.form, tc.hdr...); resp.StatusCode != 403 {
			t.Errorf("%s: POST /logout = %d, want 403", tc.name, resp.StatusCode)
		}
		if !a.signedIn() {
			t.Fatalf("%s: the session did not survive a refused logout", tc.name)
		}
	}
	if resp, _ := a.post("/logout", nil); resp.StatusCode != 303 || a.signedIn() {
		t.Fatalf("POST /logout with the token = %d, still signed in %v", resp.StatusCode, a.signedIn())
	}
	if !b.signedIn() {
		t.Error("logging out one session ended another")
	}
	notLogged(t, s, a.token, a.csrf, b.token, b.csrf)
}

// TestViewerCannotMutate is scenario 18: a signed-in viewer with a valid
// CSRF token is refused every admin action, and nothing changes.
func TestViewerCannotMutate(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	s := start(t, dataDir)
	createAdmin(t, s)
	admin := signIn(t, s, "admin", adminPassword)
	const viewerPassword = "viewer password 1"
	resp, body := admin.post("/settings/authentication/viewers", url.Values{
		"login": {"viewer"}, "password": {viewerPassword}, "confirm": {viewerPassword},
	})
	if resp.StatusCode != 303 {
		t.Fatalf("creating the viewer = %d:\n%s", resp.StatusCode, body)
	}
	d := openDB(t, dataDir)
	var viewerID string
	if err := d.Reader.QueryRow(`SELECT id FROM users WHERE login = 'viewer' AND role = 'viewer'`).Scan(&viewerID); err != nil {
		t.Fatal(err)
	}

	v := signIn(t, s, "viewer", viewerPassword)
	if resp, _ := v.get("/monitors"); resp.StatusCode != 200 {
		t.Fatalf("viewer GET /monitors = %d, want 200", resp.StatusCode)
	}
	users := `SELECT COUNT(*) FROM users`
	before := count(t, d, users)
	for _, path := range []string{
		"/settings/authentication/totp",
		"/settings/authentication/totp/disable",
		"/settings/authentication/viewers",
		"/settings/authentication/viewers/" + viewerID + "/disable",
		"/settings/authentication/viewers/" + viewerID + "/enable",
		"/settings/authentication/passkeys/x/delete",
	} {
		resp, _ := v.post(path, url.Values{"login": {"intruder"}, "password": {viewerPassword}, "confirm": {viewerPassword}})
		if resp.StatusCode != 403 {
			t.Errorf("viewer POST %s = %d, want 403", path, resp.StatusCode)
		}
	}
	if resp, _ := v.postJSON("/settings/authentication/passkeys/begin", []byte(`{"label":"x"}`), ""); resp.StatusCode != 403 {
		t.Errorf("viewer passkey registration = %d, want 403", resp.StatusCode)
	}
	for _, path := range []string{"/settings/authentication", "/settings/authentication/totp", "/settings/system"} {
		if resp, _ := v.get(path); resp.StatusCode != 403 {
			t.Errorf("viewer GET %s = %d, want 403", path, resp.StatusCode)
		}
	}
	if after := count(t, d, users); after != before {
		t.Errorf("users %d → %d after refused viewer requests", before, after)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = ? AND disabled = 0`, viewerID); n != 1 {
		t.Error("the viewer disabled itself")
	}
	if !v.signedIn() {
		t.Error("the viewer was signed out by refused requests")
	}
	notLogged(t, s, adminPassword, viewerPassword, admin.token, admin.csrf, v.token, v.csrf)
}

// TestSessionExpiry: a session past its expiry no longer authenticates
// pages or actions and its cookie is cleared.
func TestSessionExpiry(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	s := start(t, dataDir)
	createAdmin(t, s)
	c := signIn(t, s, "admin", adminPassword)
	d := openDB(t, dataDir)
	ageSessions(t, d, "expires_at", time.Minute)

	resp, _ := c.get("/monitors")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login?next=%2Fmonitors" {
		t.Fatalf("GET /monitors with an expired session = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "sinjal_session=;") {
		t.Errorf("expired cookie not cleared: Set-Cookie %q", resp.Header.Get("Set-Cookie"))
	}
	const newPassword = "never applied 123"
	if resp, _ := c.post("/account/password", url.Values{"password": {newPassword}, "confirm": {newPassword}}); resp.StatusCode != 401 {
		t.Errorf("POST /account/password with an expired session = %d, want 401", resp.StatusCode)
	}
	if tok, code := login(t, s, "admin", adminPassword); tok == "" || code != 303 {
		t.Errorf("the refused password change took effect: login %d", code)
	}
}

// TestPasswordChangeKillsSessions: changing the password ends every other
// session, rotates the current one, and only the new password signs in.
func TestPasswordChangeKillsSessions(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	s := start(t, dataDir)
	createAdmin(t, s)
	a := signIn(t, s, "admin", adminPassword)
	b := signIn(t, s, "admin", adminPassword)
	oldA := a.token

	const newPassword = "a brand new password"
	resp, body := a.post("/account/password", url.Values{"password": {newPassword}, "confirm": {newPassword}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/account/password?changed=1" {
		t.Fatalf("POST /account/password = %d %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	a.adopt(resp)
	if a.token == oldA {
		t.Fatal("the current session was not rotated")
	}
	if !a.signedIn() {
		t.Error("the rotated session does not work")
	}
	if (&client{t: t, s: s, token: oldA}).signedIn() {
		t.Error("the pre-rotation cookie still works")
	}
	if b.signedIn() {
		t.Error("another session survived the password change")
	}
	if tok, code := login(t, s, "admin", adminPassword); tok != "" || code != 401 {
		t.Errorf("old password: status %d, token issued %v", code, tok != "")
	}
	if tok, code := login(t, s, "admin", newPassword); tok == "" || code != 303 {
		t.Errorf("new password: status %d", code)
	}
	d := openDB(t, dataDir)
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.password_changed'`); n != 1 {
		t.Errorf("auth.password_changed events = %d, want 1", n)
	}
	notLogged(t, s, adminPassword, newPassword, oldA, a.token, b.token)
}

// TestReauthEnforced: once the re-authentication window has passed,
// sensitive pages and actions send the user to /reauth and do nothing;
// re-entering the password rotates the session and unlocks them.
func TestReauthEnforced(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	s := start(t, dataDir)
	createAdmin(t, s)
	c := signIn(t, s, "admin", adminPassword)
	d := openDB(t, dataDir)
	ageSessions(t, d, "reauthenticated_at", 11*time.Minute)

	if resp, _ := c.get("/account/password"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/reauth?next=%2Faccount%2Fpassword" {
		t.Errorf("stale GET /account/password = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	const viewerPassword = "viewer password 1"
	createViewer := url.Values{"login": {"viewer"}, "password": {viewerPassword}, "confirm": {viewerPassword}}
	resp, _ := c.post("/settings/authentication/viewers", createViewer, "Referer", s.base+"/settings/authentication")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/reauth?next=%2Fsettings%2Fauthentication" {
		t.Errorf("stale POST viewers = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	viewers := `SELECT COUNT(*) FROM users WHERE role = 'viewer'`
	if n := count(t, d, viewers); n != 0 {
		t.Fatal("a stale session created a viewer")
	}

	if resp, _ := c.post("/reauth", url.Values{"password": {"not the password"}, "next": {"/settings/authentication"}}); resp.StatusCode != 401 {
		t.Errorf("re-auth with a wrong password = %d, want 401", resp.StatusCode)
	}
	old := c.token
	resp, _ = c.post("/reauth", url.Values{"password": {adminPassword}, "next": {"/settings/authentication"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/settings/authentication" {
		t.Fatalf("re-auth = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c.adopt(resp)
	if (&client{t: t, s: s, token: old}).signedIn() {
		t.Error("the session was not rotated by re-authentication")
	}
	if resp, _ := c.post("/settings/authentication/viewers", createViewer); resp.StatusCode != 303 || count(t, d, viewers) != 1 {
		t.Errorf("POST viewers after re-auth = %d, viewers %d", resp.StatusCode, count(t, d, viewers))
	}
	notLogged(t, s, adminPassword, "not the password", viewerPassword, old, c.token)
}

// TestPasskeyCeremony: an admin registers a passkey through the real
// endpoints with a software authenticator and signs in with it alone; the
// ceremony cannot be replayed.
func TestPasskeyCeremony(t *testing.T) {
	skipShort(t)
	addr := freeAddr(t)
	_, port, _ := net.SplitHostPort(addr)
	origin := "http://localhost:" + port
	dataDir := filepath.Join(t.TempDir(), "data")
	s := startAt(t, dataDir, addr, "SINJAL_BASE_URL="+origin)
	s.base = origin // the RP origin: requests must carry Host localhost:port
	createAdmin(t, s)
	admin := signIn(t, s, "admin", adminPassword)

	ceremony := func(c *client, base, request string, answer func([]byte) ([]byte, error)) (finishBody []byte, ceremonyCookie string, resp *http.Response, body string) {
		t.Helper()
		begin, opts := c.postJSON(base+"/begin", []byte(request), "")
		if begin.StatusCode != 200 {
			t.Fatalf("POST %s/begin = %d: %s", base, begin.StatusCode, opts)
		}
		ceremonyCookie = cookie(begin, "sinjal_passkey")
		finishBody, err := answer([]byte(opts))
		if err != nil {
			t.Fatal(err)
		}
		resp, body = c.postJSON(base+"/finish", finishBody, ceremonyCookie)
		return finishBody, ceremonyCookie, resp, body
	}
	redirect := func(body string) string {
		var m map[string]string
		json.Unmarshal([]byte(body), &m)
		return m["redirect"]
	}

	key := passkeytest.New("localhost", origin)
	_, _, resp, body := ceremony(admin, "/settings/authentication/passkeys", `{"label":"Test key"}`, key.Create)
	if resp.StatusCode != 200 || redirect(body) != "/settings/authentication" {
		t.Fatalf("registering a passkey = %d: %s\n%s", resp.StatusCode, body, s.logs)
	}
	admin.adopt(resp)
	if _, page := admin.get("/settings/authentication"); !strings.Contains(page, "Test key") {
		t.Error("the registered passkey is not listed")
	}

	anon := &client{t: t, s: s}
	finish, cer, resp, body := ceremony(anon, "/login/passkey", `{"next":"/monitors"}`, key.Get)
	if resp.StatusCode != 200 || redirect(body) != "/monitors" {
		t.Fatalf("passkey sign-in = %d: %s", resp.StatusCode, body)
	}
	signed := &client{t: t, s: s, token: cookie(resp, "sinjal_session")}
	if signed.token == "" || !signed.signedIn() {
		t.Fatal("passkey sign-in issued no working session")
	}
	if resp, body := anon.postJSON("/login/passkey/finish", finish, cer); resp.StatusCode != 400 || cookie(resp, "sinjal_session") != "" {
		t.Errorf("replayed passkey sign-in = %d: %s", resp.StatusCode, body)
	}
	d := openDB(t, dataDir)
	if n := count(t, d, `SELECT COUNT(*) FROM passkeys`); n != 1 {
		t.Errorf("passkeys stored = %d, want 1", n)
	}
	notLogged(t, s, adminPassword, admin.token, signed.token, cer)
}

// totpCode is an independent RFC 6238 implementation (SHA-1, 6 digits,
// 30 s), so the test does not trust the code under test to check itself.
func totpCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(secret, " ", ""))
	if err != nil {
		t.Fatalf("setup key %q: %v", secret, err)
	}
	var msg [8]byte
	byteorder.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[19] & 0xf
	return fmt.Sprintf("%06d", (byteorder.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

var (
	totpSecretRe  = regexp.MustCompile(`<code class="totp-secret">([A-Z2-7 ]+)</code>`)
	totpPendingRe = regexp.MustCompile(`name="pending" value="([^"]+)"`)
	challengeRe   = regexp.MustCompile(`name="challenge" value="([^"]+)"`)
)

func scrape(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page lacks %s:\n%s", re, body)
	}
	return m[1]
}

// TestTOTPReplay: with TOTP enabled a password alone does not sign in, and
// every code works once only, including the code used to enrol.
func TestTOTPReplay(t *testing.T) {
	skipShort(t)
	s := start(t, filepath.Join(t.TempDir(), "data"))
	createAdmin(t, s)
	admin := signIn(t, s, "admin", adminPassword)

	_, page := admin.get("/settings/authentication/totp")
	secret := scrape(t, totpSecretRe, page)
	pending := scrape(t, totpPendingRe, page)
	now := time.Now()
	enrolCode := totpCode(t, secret, now)
	resp, body := admin.post("/settings/authentication/totp", url.Values{"pending": {pending}, "code": {enrolCode}})
	if resp.StatusCode != 303 {
		t.Fatalf("enabling TOTP = %d:\n%s", resp.StatusCode, body)
	}

	// challenge signs in with the password and returns the code step's token.
	challenge := func() string {
		t.Helper()
		resp, body := (&client{t: t, s: s}).post("/login", url.Values{"login": {"admin"}, "password": {adminPassword}})
		if resp.StatusCode != 200 || cookie(resp, "sinjal_session") != "" {
			t.Fatalf("password step with TOTP on = %d, session issued %v", resp.StatusCode, cookie(resp, "sinjal_session") != "")
		}
		return scrape(t, challengeRe, body)
	}
	submit := func(ch, code string) *http.Response {
		t.Helper()
		resp, _ := (&client{t: t, s: s}).post("/login/totp", url.Values{"challenge": {ch}, "code": {code}})
		return resp
	}

	ch := challenge()
	if resp := submit(ch, enrolCode); resp.StatusCode != 401 || cookie(resp, "sinjal_session") != "" {
		t.Errorf("the enrolment code reused at login = %d, want 401", resp.StatusCode)
	}
	next := totpCode(t, secret, now.Add(30*time.Second))
	resp = submit(challenge(), next)
	if resp.StatusCode != 303 || cookie(resp, "sinjal_session") == "" {
		t.Fatalf("a fresh code = %d, want 303 with a session", resp.StatusCode)
	}
	if resp := submit(challenge(), next); resp.StatusCode != 401 || cookie(resp, "sinjal_session") != "" {
		t.Errorf("a code used for sign-in reused = %d, want 401", resp.StatusCode)
	}
	notLogged(t, s, adminPassword, secret, strings.ReplaceAll(secret, " ", ""), pending, ch, admin.token, cookie(resp, "sinjal_session"))
}
