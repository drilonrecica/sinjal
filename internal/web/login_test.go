package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
)

const testPassword = "correct horse battery"

func loginForm(login, password, next string) url.Values {
	v := url.Values{"login": {login}, "password": {password}}
	if next != "" {
		v.Set("next", next)
	}
	return v
}

// sessionCookie returns the session cookie a response set, or nil.
func sessionCookie(rec interface{ Result() *http.Response }) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == plainSessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginFormRenders(t *testing.T) {
	e := newAppEnv(t)
	rec := e.serve(req("GET", "/login?next=/monitors", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("GET /login = %d", rec.Code)
	}
	for _, want := range []string{
		`<form class="auth-form" method="post" action="/login">`,
		`<input type="hidden" name="next" value="/monitors">`,
		`autocomplete="username"`, `autocomplete="current-password"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page lacks %s", want)
		}
	}
	if body := e.serve(req("GET", "/login?next=//evil.example", nil)).Body.String(); strings.Contains(body, `name="next"`) {
		t.Error("an unsafe next value is carried into the form")
	}
}

func TestLoginSuccess(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)

	rec := e.serve(req("POST", "/login", loginForm("admin", testPassword, "/monitors?x=1")))
	if rec.Code != 303 || rec.Header().Get("Location") != "/monitors?x=1" {
		t.Fatalf("POST /login = %d %q, want 303 to next", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(rec)
	if c == nil || !c.HttpOnly {
		t.Fatalf("no session cookie: %v", rec.Result().Cookies())
	}
	_, u, err := e.sessions.Lookup(context.Background(), c.Value, time.Now())
	if err != nil || u.ID != "u1" {
		t.Fatalf("issued session: %+v %v", u, err)
	}

	// Signed in, /login sends the user on.
	if rec := e.serve(withCookie(req("GET", "/login", nil), c.Value)); rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Errorf("GET /login signed in = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if logs := e.logs.String(); strings.Contains(logs, testPassword) || strings.Contains(logs, c.Value) {
		t.Error("password or session token reached the log")
	}
}

func TestLoginRejectsUnsafeNext(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)
	for _, next := range []string{"//evil.example", "https://evil.example/", `/\evil.example`} {
		rec := e.serve(req("POST", "/login", loginForm("admin", testPassword, next)))
		if rec.Code != 303 || rec.Header().Get("Location") != "/" {
			t.Errorf("next=%q: %d %q, want 303 to /", next, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestLoginFailureIsGeneric(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)
	var bodies []string
	for _, f := range []url.Values{
		loginForm("admin", "wrong password!", ""),
		loginForm("nobody", testPassword, ""),
	} {
		rec := e.serve(req("POST", "/login", f))
		if rec.Code != 401 || sessionCookie(rec) != nil {
			t.Fatalf("failed login = %d, cookie %v", rec.Code, sessionCookie(rec))
		}
		body := rec.Body.String()
		if !strings.Contains(body, loginFailedMsg) || strings.Contains(body, testPassword) || strings.Contains(body, "wrong password!") {
			t.Errorf("failure page: %s", body)
		}
		bodies = append(bodies, strings.ReplaceAll(body, f.Get("login"), "LOGIN"))
	}
	if bodies[0] != bodies[1] {
		t.Error("wrong password and unknown user render different pages")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)
	for i := range loginMaxFailures {
		if rec := e.serve(req("POST", "/login", loginForm("admin", "wrong password!", ""))); rec.Code != 401 {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}
	// Blocked now, even with the right password and with different case.
	rec := e.serve(req("POST", "/login", loginForm("ADMIN", testPassword, "")))
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || sessionCookie(rec) != nil {
		t.Fatalf("after %d failures = %d, want 429 without a session", loginMaxFailures, rec.Code)
	}
	rec = e.serve(req("POST", "/login", loginForm("admin", testPassword, "")))
	if rec.Code != 429 {
		t.Fatalf("right password while blocked = %d, want 429", rec.Code)
	}
	// Another client address is not affected.
	r := req("POST", "/login", loginForm("admin", testPassword, ""))
	r.RemoteAddr = "198.51.100.7:1234"
	if rec := e.serve(r); rec.Code != 303 {
		t.Errorf("other client = %d, want 303", rec.Code)
	}
	var failed int
	e.db.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.login_failed'`).Scan(&failed)
	if failed != loginMaxFailures {
		t.Errorf("audited failures = %d, want %d (blocked attempts are not checked)", failed, loginMaxFailures)
	}
}

func TestLoginReplacesAnExistingSession(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", testPassword)
	e.addUser(t, "u2", "other", "viewer", testPassword)
	old, sess := e.signIn(t, "u2")

	r := withCookie(req("POST", "/login", loginForm("admin", testPassword, "")), old)
	r.Header.Set(CSRFHeader, e.csrf.token(sess.ID))
	rec := e.serve(r)
	if rec.Code != 303 || sessionCookie(rec) == nil {
		t.Fatalf("login over an existing session = %d", rec.Code)
	}
	if _, _, err := e.sessions.Lookup(context.Background(), old, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("previous session survived: %v", err)
	}
}
