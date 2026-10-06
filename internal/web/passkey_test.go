package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/auth/passkeytest"
)

// passkeyEnv drives the ceremonies the way js/passkey.js does: JSON posts
// to begin and finish at SINJAL_BASE_URL's host, carrying the ceremony
// cookie from one to the other.
type passkeyEnv struct {
	*totpEnv // signed-in admin u1
}

func newPasskeyEnv(t *testing.T) *passkeyEnv {
	t.Helper()
	return &passkeyEnv{newTOTPEnv(t)}
}

func newAuthenticator() *passkeytest.Authenticator {
	return passkeytest.New("localhost", testBaseURL)
}

// post sends a JSON request to the base URL's host. session "" is anonymous.
func (e *passkeyEnv) postJSON(path string, body []byte, session, ceremony string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	r.Host = "localhost"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if session != "" {
		r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: session})
		if session == e.cookie {
			r.Header.Set(CSRFHeader, e.csrf.token(e.sess.ID))
		}
	}
	if ceremony != "" {
		r.AddCookie(&http.Cookie{Name: plainPasskeyCookie, Value: ceremony})
	}
	return e.serve(r)
}

func ceremonyCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == plainPasskeyCookie {
			return c
		}
	}
	t.Fatalf("no ceremony cookie; response %d: %s", rec.Code, rec.Body)
	return nil
}

func jsonField(t *testing.T, rec *httptest.ResponseRecorder, field string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response %d is not a JSON object: %s", rec.Code, rec.Body)
	}
	return m[field]
}

// ceremony runs begin, lets answer produce the authenticator's response and
// posts it to finish.
func (e *passkeyEnv) ceremony(t *testing.T, base, request, session string, answer func([]byte) ([]byte, error)) *httptest.ResponseRecorder {
	t.Helper()
	begin := e.postJSON(base+"/begin", []byte(request), session, "")
	if begin.Code != 200 {
		t.Fatalf("POST %s/begin = %d: %s", base, begin.Code, begin.Body)
	}
	resp, err := answer(begin.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return e.postJSON(base+"/finish", resp, session, ceremonyCookie(t, begin).Value)
}

// register adds a passkey for the signed-in admin and adopts the rotated session.
func (e *passkeyEnv) register(t *testing.T, label string) *passkeytest.Authenticator {
	t.Helper()
	a := newAuthenticator()
	rec := e.ceremony(t, "/settings/authentication/passkeys", fmt.Sprintf(`{"label":%q}`, label), e.cookie, a.Create)
	if rec.Code != 200 || jsonField(t, rec, "redirect") != "/settings/authentication" {
		t.Fatalf("adding a passkey = %d: %s\n%s", rec.Code, rec.Body, e.logs)
	}
	e.follow(t, rec)
	return a
}

func TestPasskeyRegistrationFlow(t *testing.T) {
	e := newPasskeyEnv(t)
	other, _ := e.signIn(t, "u1")

	if body := e.get("/settings/authentication").Body.String(); !strings.Contains(body, "<strong>None yet.</strong>") ||
		!strings.Contains(body, `data-passkey="create" data-begin="/settings/authentication/passkeys/begin"`) || !strings.Contains(body, "/static/js/passkey.") {
		t.Fatalf("settings page does not offer to add a passkey:\n%s", body)
	}

	begin := e.postJSON("/settings/authentication/passkeys/begin", []byte(`{"label":"Laptop"}`), e.cookie, "")
	c := ceremonyCookie(t, begin)
	if begin.Code != 200 || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 300 ||
		begin.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(begin.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("begin = %d, cookie %+v, headers %v", begin.Code, c, begin.Header())
	}
	a := newAuthenticator()
	resp, _ := a.Create(begin.Body.Bytes())
	old := e.cookie
	rec := e.postJSON("/settings/authentication/passkeys/finish", resp, e.cookie, c.Value)
	if rec.Code != 200 || jsonField(t, rec, "redirect") != "/settings/authentication" {
		t.Fatalf("finish = %d: %s\n%s", rec.Code, rec.Body, e.logs)
	}
	e.follow(t, rec)
	for name, cookie := range map[string]string{"previous": old, "other": other} {
		if _, _, err := e.sessions.Lookup(context.Background(), cookie, time.Now()); !errors.Is(err, auth.ErrNoSession) {
			t.Errorf("%s session survived adding a passkey: %v", name, err)
		}
	}
	// A finished ceremony is gone: the same answer cannot be posted again.
	if rec := e.postJSON("/settings/authentication/passkeys/finish", resp, e.cookie, c.Value); rec.Code != 400 || !strings.Contains(jsonField(t, rec, "error"), "expired") {
		t.Errorf("replayed finish = %d: %s", rec.Code, rec.Body)
	}

	e.register(t, "")
	body := e.get("/settings/authentication").Body.String()
	for _, want := range []string{`<span class="passkey-label">Laptop</span>`, `<span class="passkey-label">Passkey</span>`, "Never used", `aria-label="Remove passkey Laptop"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page lacks %s", want)
		}
	}
	if e.audited(t, "auth.passkey_added") != 2 {
		t.Error("adding passkeys was not audited")
	}

	if rec := e.postJSON("/settings/authentication/passkeys/begin", []byte(`{"label":"`+strings.Repeat("x", 65)+`"}`), e.cookie, ""); rec.Code != 422 || jsonField(t, rec, "error") == "" {
		t.Errorf("overlong label = %d: %s", rec.Code, rec.Body)
	}
}

func TestPasskeyRegistrationGuards(t *testing.T) {
	e := newPasskeyEnv(t)
	begin := "/settings/authentication/passkeys/begin"

	// No CSRF token.
	r := httptest.NewRequest("POST", begin, strings.NewReader("{}"))
	r.Host = "localhost"
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: e.cookie})
	if rec := e.serve(r); rec.Code != 403 {
		t.Errorf("begin without a CSRF token = %d, want 403", rec.Code)
	}
	// A viewer.
	e.addUser(t, "v1", "viewer", "viewer", testPassword)
	viewer, vsess := e.signIn(t, "v1")
	r = httptest.NewRequest("POST", begin, strings.NewReader("{}"))
	r.Host = "localhost"
	r.Header.Set(CSRFHeader, e.csrf.token(vsess.ID))
	r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: viewer})
	if rec := e.serve(r); rec.Code != 403 {
		t.Errorf("viewer begin = %d, want 403", rec.Code)
	}
	// Without recent authentication: sent to /reauth, nothing begun.
	a := e.register(t, "Laptop")
	old := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	var id string
	e.db.Reader.QueryRow(`SELECT id FROM passkeys`).Scan(&id)
	for _, path := range []string{begin, "/settings/authentication/passkeys/finish"} {
		rec := e.postJSON(path, []byte("{}"), e.cookie, "")
		if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
			t.Errorf("stale POST %s = %d %q, want 303 to /reauth", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := e.post("/settings/authentication/passkeys/"+id+"/delete", url.Values{}); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
		t.Errorf("stale delete = %d %q, want 303 to /reauth", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	e.db.Reader.QueryRow(`SELECT COUNT(*) FROM passkeys`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d passkeys left after a delete without recent authentication", n)
	}
	_ = a
}

func TestPasskeyLoginFlow(t *testing.T) {
	e := newPasskeyEnv(t)
	if body := e.serve(req("GET", "/login", nil)).Body.String(); strings.Contains(body, "data-passkey") || strings.Contains(body, "passkey.") {
		t.Error("login page offers a passkey before one exists")
	}
	a := e.register(t, "Laptop")
	body := e.serve(req("GET", "/login?next=/monitors", nil)).Body.String()
	for _, want := range []string{`data-passkey="get" data-begin="/login/passkey/begin" data-finish="/login/passkey/finish" data-next="/monitors"`, "Sign in with a passkey", "/static/js/passkey."} {
		if !strings.Contains(body, want) {
			t.Errorf("login page lacks %s", want)
		}
	}

	// Anonymous sign-in. An earlier session in the same browser is ended.
	begin := e.postJSON("/login/passkey/begin", []byte(`{"next":"/monitors"}`), "", "")
	resp, _ := a.Get(begin.Body.Bytes())
	cer := ceremonyCookie(t, begin).Value
	rec := e.postJSON("/login/passkey/finish", resp, "", cer)
	if rec.Code != 200 || jsonField(t, rec, "redirect") != "/monitors" {
		t.Fatalf("passkey sign-in = %d: %s\n%s", rec.Code, rec.Body, e.logs)
	}
	c := sessionCookie(rec)
	if c == nil || !c.HttpOnly {
		t.Fatal("no session cookie")
	}
	if _, u, err := e.sessions.Lookup(context.Background(), c.Value, time.Now()); err != nil || u.ID != "u1" {
		t.Fatalf("issued session: %+v %v", u, err)
	}
	if e.audited(t, "auth.login_succeeded") != 1 {
		t.Error("the sign-in was not audited")
	}
	// Single use: the answer cannot be replayed, with or without the cookie.
	for _, ceremony := range []string{cer, ""} {
		if rec := e.postJSON("/login/passkey/finish", resp, "", ceremony); rec.Code != 400 || sessionCookie(rec) != nil {
			t.Errorf("replayed sign-in = %d, want 400 without a session", rec.Code)
		}
	}
	// An unsafe next is dropped.
	rec = e.ceremony(t, "/login/passkey", `{"next":"//evil.example"}`, "", a.Get)
	if rec.Code != 200 || jsonField(t, rec, "redirect") != "/" {
		t.Errorf("unsafe next → %d %s", rec.Code, rec.Body)
	}
	// A sign-in ceremony cannot be finished as a re-authentication.
	begin = e.postJSON("/login/passkey/begin", nil, "", "")
	resp, _ = a.Get(begin.Body.Bytes())
	if rec := e.postJSON("/reauth/passkey/finish", resp, e.cookie, ceremonyCookie(t, begin).Value); rec.Code != 401 {
		t.Errorf("sign-in ceremony finished at /reauth = %d, want 401", rec.Code)
	}
	if logs := e.logs.String(); strings.Contains(logs, c.Value) || strings.Contains(logs, cer) {
		t.Error("a session token or ceremony id reached the log")
	}
}

func TestPasskeyLoginRateLimit(t *testing.T) {
	e := newPasskeyEnv(t)
	a := e.register(t, "")
	bad := *a
	bad.UV = false // never accepted
	for i := range loginMaxFailures {
		rec := e.ceremony(t, "/login/passkey", "{}", "", bad.Get)
		if rec.Code != 401 || sessionCookie(rec) != nil || jsonField(t, rec, "error") != "That passkey was not accepted." {
			t.Fatalf("attempt %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}
	rec := e.postJSON("/login/passkey/begin", nil, "", "")
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("begin after %d failures = %d, want 429", loginMaxFailures, rec.Code)
	}
	if n := e.audited(t, "auth.login_failed"); n != loginMaxFailures {
		t.Errorf("audited failures = %d, want %d", n, loginMaxFailures)
	}
	// Another address is not affected.
	r := httptest.NewRequest("POST", "/login/passkey/begin", nil)
	r.Host, r.RemoteAddr = "localhost", "198.51.100.7:1234"
	if rec := e.serve(r); rec.Code != 200 {
		t.Errorf("other client = %d, want 200", rec.Code)
	}
}

func TestPasskeyReauthFlow(t *testing.T) {
	e := newPasskeyEnv(t)
	if body := e.get("/reauth").Body.String(); strings.Contains(body, "data-passkey") {
		t.Error("re-auth page offers a passkey before one exists")
	}
	if rec := e.postJSON("/reauth/passkey/begin", nil, e.cookie, ""); rec.Code != 400 || !strings.Contains(jsonField(t, rec, "error"), "no passkey") {
		t.Errorf("begin without a passkey = %d: %s", rec.Code, rec.Body)
	}
	a := e.register(t, "Laptop")
	old := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	body := e.get("/reauth?next=/settings/authentication/totp").Body.String()
	if !strings.Contains(body, `data-begin="/reauth/passkey/begin" data-finish="/reauth/passkey/finish" data-next="/settings/authentication/totp"`) ||
		!strings.Contains(body, "Use a passkey instead") {
		t.Fatalf("re-auth page does not offer the passkey:\n%s", body)
	}
	if rec := e.get("/settings/authentication/totp"); rec.Code != 303 {
		t.Fatalf("setup with a stale session = %d, want 303", rec.Code)
	}

	// Someone else's passkey does not confirm this session.
	e.addUser(t, "u2", "second", "admin", testPassword)
	stranger := newAuthenticator()
	if rec := e.ceremony(t, "/reauth/passkey", "{}", e.cookie, stranger.Get); rec.Code != 401 || sessionCookie(rec) != nil {
		t.Errorf("unknown passkey = %d, want 401", rec.Code)
	}

	previous := e.cookie
	rec := e.ceremony(t, "/reauth/passkey", `{"next":"/settings/authentication/totp"}`, e.cookie, a.Get)
	if rec.Code != 200 || jsonField(t, rec, "redirect") != "/settings/authentication/totp" {
		t.Fatalf("passkey re-auth = %d: %s\n%s", rec.Code, rec.Body, e.logs)
	}
	e.follow(t, rec)
	if _, _, err := e.sessions.Lookup(context.Background(), previous, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("the session was not rotated: %v", err)
	}
	if rec := e.get("/settings/authentication/totp"); rec.Code != 200 {
		t.Errorf("setup after passkey re-auth = %d, want 200", rec.Code)
	}
	if e.audited(t, "auth.reauthenticated") != 1 || e.audited(t, "auth.reauth_failed") != 1 {
		t.Error("re-authentication outcomes were not audited")
	}
}

func TestPasskeyDeleteFlow(t *testing.T) {
	e := newPasskeyEnv(t)
	a := e.register(t, "Laptop")
	var id string
	e.db.Reader.QueryRow(`SELECT id FROM passkeys`).Scan(&id)
	other, _ := e.signIn(t, "u1")

	// Another admin cannot remove it.
	e.addUser(t, "u2", "second", "admin", testPassword)
	second, ssess := e.signIn(t, "u2")
	r := withCookie(req("POST", "/settings/authentication/passkeys/"+id+"/delete", url.Values{CSRFFormField: {e.csrf.token(ssess.ID)}}), second)
	if rec := e.serve(r); rec.Code != 404 {
		t.Errorf("another admin's delete = %d, want 404", rec.Code)
	}

	rec := e.post("/settings/authentication/passkeys/"+id+"/delete", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Fatalf("delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	e.follow(t, rec)
	if _, _, err := e.sessions.Lookup(context.Background(), other, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("other session survived removing a passkey: %v", err)
	}
	if e.audited(t, "auth.passkey_removed") != 1 {
		t.Error("the removal was not audited")
	}
	if body := e.get("/settings/authentication").Body.String(); !strings.Contains(body, "None yet.") {
		t.Error("the removed passkey is still listed")
	}
	if rec := e.ceremony(t, "/login/passkey", "{}", "", a.Get); rec.Code != 401 || sessionCookie(rec) != nil {
		t.Errorf("sign-in with the removed passkey = %d, want 401", rec.Code)
	}
}

// The usual reverse-proxy mistake: Sinjal is opened at an address other than
// SINJAL_BASE_URL. The answer names both.
func TestPasskeyWrongAddress(t *testing.T) {
	e := newPasskeyEnv(t)
	for _, path := range []string{"/login/passkey/begin", "/reauth/passkey/begin", "/settings/authentication/passkeys/begin"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		r.Header.Set(CSRFHeader, e.csrf.token(e.sess.ID))
		r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: e.cookie})
		rec := e.serve(r) // Host: example.com
		msg := jsonField(t, rec, "error")
		if rec.Code != 400 {
			t.Errorf("%s at the wrong address = %d, want 400", path, rec.Code)
		}
		for _, want := range []string{"http://localhost", "http://example.com", "SINJAL_BASE_URL", "SINJAL_TRUSTED_PROXIES"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message %q lacks %s", path, msg, want)
			}
		}
	}
	if !strings.Contains(e.logs.String(), "passkey ceremony from an address other than SINJAL_BASE_URL") {
		t.Error("the mismatch was not logged")
	}
}

func TestPasskeysUnavailableWithoutBaseURL(t *testing.T) {
	e := newAppEnvAt(t, "")
	e.addUser(t, "u1", "admin", "admin", testPassword)
	cookie, sess := e.signIn(t, "u1")

	body := e.serve(withCookie(req("GET", "/settings/authentication", nil), cookie)).Body.String()
	if !strings.Contains(body, "SINJAL_BASE_URL is not set") || strings.Contains(body, "data-passkey") {
		t.Errorf("settings page does not explain why passkeys are off:\n%s", body)
	}
	for _, path := range []string{"/login/passkey/begin", "/reauth/passkey/begin", "/settings/authentication/passkeys/begin"} {
		r := withCookie(req("POST", path, nil), cookie)
		r.Header.Set(CSRFHeader, e.csrf.token(sess.ID))
		if rec := e.serve(r); rec.Code != 404 || !strings.Contains(rec.Body.String(), "not available") {
			t.Errorf("%s without a base URL = %d: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestCeremonyStore(t *testing.T) {
	s := newCeremonyStore()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id := s.put(pendingCeremony{next: "/a"}, t0)
	if p, ok := s.take(id, t0.Add(passkeyCeremonyTTL-time.Second)); !ok || p.next != "/a" {
		t.Fatalf("take = %+v, %v", p, ok)
	}
	if _, ok := s.take(id, t0); ok {
		t.Error("a ceremony was taken twice")
	}
	id = s.put(pendingCeremony{}, t0)
	if _, ok := s.take(id, t0.Add(passkeyCeremonyTTL)); ok {
		t.Error("an expired ceremony was returned")
	}
	if _, ok := s.take("unknown", t0); ok {
		t.Error("an unknown id was found")
	}

	// Full: expired entries go first, then the oldest.
	first := s.put(pendingCeremony{}, t0)
	for i := 1; i < passkeyCeremonyCap; i++ {
		s.put(pendingCeremony{}, t0.Add(time.Duration(i)*time.Millisecond))
	}
	s.put(pendingCeremony{}, t0.Add(time.Second))
	if len(s.entries) != passkeyCeremonyCap {
		t.Errorf("store holds %d entries, want %d", len(s.entries), passkeyCeremonyCap)
	}
	if _, ok := s.take(first, t0.Add(time.Second)); ok {
		t.Error("the oldest ceremony was not the one dropped")
	}
	s.put(pendingCeremony{}, t0.Add(passkeyCeremonyTTL+time.Minute))
	if len(s.entries) != 1 {
		t.Errorf("expired ceremonies were kept: %d entries", len(s.entries))
	}
}
