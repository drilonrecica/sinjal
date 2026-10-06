package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
)

type reauthEnv struct {
	*appEnv
	cookie string
	sess   auth.Session
}

// newReauthEnv signs in an admin and mounts /sensitive (GET and POST)
// behind RequireRecentAuth.
func newReauthEnv(t *testing.T) reauthEnv {
	t.Helper()
	e := newAppEnv(t)
	logger, _ := quietLogger()
	e.h.(*chi.Mux).With(LoadSession(e.sessions, logger), RequireAuth(logger), RequireRecentAuth(logger, time.Now)).
		Group(func(r chi.Router) {
			ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
			r.Get("/sensitive", ok)
			r.Post("/sensitive", ok)
		})
	e.addUser(t, "u1", "admin", "admin", testPassword)
	cookie, sess := e.signIn(t, "u1")
	return reauthEnv{appEnv: e, cookie: cookie, sess: sess}
}

// age moves the session's last proof of credentials past the window.
func (e reauthEnv) age(t *testing.T) {
	t.Helper()
	old := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ? WHERE id = ?`, old, e.sess.ID); err != nil {
		t.Fatal(err)
	}
}

func (e reauthEnv) reauth(password, next string) *http.Request {
	f := url.Values{"password": {password}, CSRFFormField: {e.csrf.token(e.sess.ID)}}
	if next != "" {
		f.Set("next", next)
	}
	return withCookie(req("POST", "/reauth", f), e.cookie)
}

func TestRequireRecentAuth(t *testing.T) {
	e := newReauthEnv(t)
	if rec := e.serve(withCookie(req("GET", "/sensitive", nil), e.cookie)); rec.Code != 204 {
		t.Fatalf("fresh login = %d, want 204 (a login counts as re-authentication)", rec.Code)
	}
	e.age(t)

	rec := e.serve(withCookie(req("GET", "/sensitive?a=1", nil), e.cookie))
	if rec.Code != 303 || rec.Header().Get("Location") != "/reauth?next=%2Fsensitive%3Fa%3D1" {
		t.Errorf("stale GET = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	for referer, want := range map[string]string{
		"http://example.com/settings/authentication?tab=1": "/reauth?next=%2Fsettings%2Fauthentication%3Ftab%3D1",
		"http://evil.example/settings":                     "/reauth",
		"":                                                 "/reauth",
	} {
		r := withCookie(req("POST", "/sensitive", url.Values{}), e.cookie)
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		rec := e.serve(r)
		if rec.Code != 303 || rec.Header().Get("Location") != want {
			t.Errorf("stale POST, Referer %q = %d %q, want %q", referer, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	r := withCookie(req("GET", "/sensitive", nil), e.cookie)
	r.Header.Set("HX-Request", "true")
	if rec := e.serve(r); rec.Code != 403 || rec.Header().Get("HX-Redirect") != "/reauth?next=%2Fsensitive" {
		t.Errorf("stale htmx = %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}

func TestReauthFormRenders(t *testing.T) {
	e := newReauthEnv(t)
	body := e.serve(withCookie(req("GET", "/reauth?next=/sensitive", nil), e.cookie)).Body.String()
	for _, want := range []string{
		`<form class="auth-form" method="post" action="/reauth">`,
		`<input type="hidden" name="next" value="/sensitive">`,
		`<input type="hidden" name="_csrf" value="` + e.csrf.token(e.sess.ID) + `">`,
		`Password for admin`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("re-auth page lacks %s", want)
		}
	}
}

func TestReauthSuccessRotatesTheSession(t *testing.T) {
	e := newReauthEnv(t)
	e.age(t)

	rec := e.serve(e.reauth(testPassword, "/sensitive"))
	if rec.Code != 303 || rec.Header().Get("Location") != "/sensitive" {
		t.Fatalf("POST /reauth = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(rec)
	if c == nil || c.Value == e.cookie {
		t.Fatal("no new session cookie")
	}
	if _, _, err := e.sessions.Lookup(context.Background(), e.cookie, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("old session still valid: %v", err)
	}
	if rec := e.serve(withCookie(req("GET", "/sensitive", nil), c.Value)); rec.Code != 204 {
		t.Errorf("after re-auth = %d, want 204", rec.Code)
	}
	// The CSRF token belonged to the old session.
	r := withCookie(req("POST", "/logout", url.Values{CSRFFormField: {e.csrf.token(e.sess.ID)}}), c.Value)
	if rec := e.serve(r); rec.Code != 403 {
		t.Errorf("old CSRF token after rotation = %d, want 403", rec.Code)
	}
	var n int
	e.db.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.reauthenticated' AND user_id = 'u1'`).Scan(&n)
	if n != 1 {
		t.Errorf("auth.reauthenticated audited %d times, want 1", n)
	}
}

func TestReauthRejectsUnsafeNext(t *testing.T) {
	e := newReauthEnv(t)
	if rec := e.serve(e.reauth(testPassword, "//evil.example")); rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Errorf("next=//evil.example → %d %q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestReauthWrongPassword(t *testing.T) {
	e := newReauthEnv(t)
	e.age(t)
	for i := range loginMaxFailures {
		rec := e.serve(e.reauth("wrong password!", ""))
		if rec.Code != 401 || sessionCookie(rec) != nil || !strings.Contains(rec.Body.String(), "Incorrect password.") {
			t.Fatalf("attempt %d = %d", i+1, rec.Code)
		}
	}
	if _, _, err := e.sessions.Lookup(context.Background(), e.cookie, time.Now()); err != nil {
		t.Fatalf("failed re-auth ended the session: %v", err)
	}
	if rec := e.serve(e.reauth(testPassword, "")); rec.Code != 429 || sessionCookie(rec) != nil {
		t.Errorf("right password after %d failures = %d, want 429", loginMaxFailures, rec.Code)
	}
	var n int
	e.db.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.reauth_failed'`).Scan(&n)
	if n != loginMaxFailures {
		t.Errorf("auth.reauth_failed audited %d times, want %d", n, loginMaxFailures)
	}
}
