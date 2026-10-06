package web

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
)

func TestSessionCookieAttributes(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	expires := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		remote     string
		tls        bool
		proto      string
		wantName   string
		wantSecure bool
	}{
		{"plain http", "192.0.2.1:1", false, "", "sinjal_session", false},
		{"direct tls", "192.0.2.1:1", true, "", "__Host-sinjal_session", true},
		{"trusted proxy https", "10.0.0.2:1", false, "https", "__Host-sinjal_session", true},
		{"untrusted peer claims https", "192.0.2.1:1", false, "https", "sinjal_session", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := quietLogger()
			r := NewRouter(logger, trusted)
			r.Get("/set", func(w http.ResponseWriter, req *http.Request) { SetSessionCookie(w, req, "tok", expires) })
			r.Get("/clear", func(w http.ResponseWriter, req *http.Request) { ClearSessionCookie(w, req) })

			for _, path := range []string{"/set", "/clear"} {
				req := httptest.NewRequest("GET", path, nil)
				req.RemoteAddr = tt.remote
				if tt.tls {
					req.TLS = &tls.ConnectionState{}
				}
				if tt.proto != "" {
					req.Header.Set("X-Forwarded-Proto", tt.proto)
				}
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				raw := rec.Header().Get("Set-Cookie")
				c, err := http.ParseSetCookie(raw)
				if err != nil {
					t.Fatalf("%s: Set-Cookie %q: %v", path, raw, err)
				}
				if c.Name != tt.wantName || c.Secure != tt.wantSecure || !c.HttpOnly ||
					c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
					t.Errorf("%s: cookie = %q", path, raw)
				}
				if path == "/set" && (c.Value != "tok" || !c.Expires.Equal(expires)) {
					t.Errorf("set cookie = %q", raw)
				}
				if path == "/clear" && (c.Value != "" || c.MaxAge >= 0) {
					t.Errorf("clear cookie = %q", raw)
				}
			}
		})
	}
}

type sessionWebEnv struct {
	h        http.Handler
	sessions *auth.Sessions
	token    string
	sess     auth.Session
	seen     *CurrentSession
}

func newSessionWebEnv(t *testing.T) *sessionWebEnv {
	t.Helper()
	logger, _ := quietLogger()
	d := migratedDB(t)
	if _, err := d.Writer.Exec(`INSERT INTO users (id, login, role, created_at, updated_at)
		VALUES ('u1', 'admin', 'admin', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	e := &sessionWebEnv{sessions: auth.NewSessions(d, logger)}
	var err error
	if e.token, e.sess, err = e.sessions.Create(context.Background(), "u1", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	r := NewRouter(logger, nil)
	r.With(LoadSession(e.sessions, logger)).Get("/who", func(w http.ResponseWriter, req *http.Request) {
		e.seen = nil
		if cs, ok := SessionFromContext(req.Context()); ok {
			e.seen = &cs
		}
	})
	RegisterLogout(r, e.sessions, logger)
	e.h = r
	return e
}

func (e *sessionWebEnv) do(method, path, cookieName, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookieName != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: value})
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestLoadSession(t *testing.T) {
	e := newSessionWebEnv(t)

	e.do("GET", "/who", "sinjal_session", e.token)
	if e.seen == nil || e.seen.User.Login != "admin" || e.seen.Session.ID != e.sess.ID {
		t.Fatalf("valid cookie: session = %+v", e.seen)
	}

	rec := e.do("GET", "/who", "", "")
	if e.seen != nil || rec.Header().Get("Set-Cookie") != "" {
		t.Error("no cookie: expected anonymous request, no Set-Cookie")
	}

	// Over plain HTTP the __Host- cookie is not the session cookie.
	e.do("GET", "/who", "__Host-sinjal_session", e.token)
	if e.seen != nil {
		t.Error("__Host- cookie accepted over plain HTTP")
	}

	rec = e.do("GET", "/who", "sinjal_session", "stale")
	if e.seen != nil || rec.Code != 200 {
		t.Errorf("stale cookie: code %d, session %+v", rec.Code, e.seen)
	}
	if c, err := http.ParseSetCookie(rec.Header().Get("Set-Cookie")); err != nil || c.MaxAge >= 0 {
		t.Errorf("stale cookie not cleared: %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestLogout(t *testing.T) {
	e := newSessionWebEnv(t)

	rec := e.do("POST", "/logout", "sinjal_session", e.token)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("cookie not cleared: %q", rec.Header().Get("Set-Cookie"))
	}
	if _, _, err := e.sessions.Lookup(context.Background(), e.token, time.Now()); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survived logout: %v", err)
	}

	if rec := e.do("POST", "/logout", "", ""); rec.Code != 303 {
		t.Errorf("logout without a session = %d, want 303", rec.Code)
	}
	if rec := e.do("GET", "/logout", "sinjal_session", e.token); rec.Code != 405 {
		t.Errorf("GET /logout = %d, want 405", rec.Code)
	}
}
