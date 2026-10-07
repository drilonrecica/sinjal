package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M7-10, scenario 17 in process: one fixture of private values, every
// access mode, every address a page has (its slug, its token, a mapped
// hostname) and every format. Nothing private appears anywhere, and a page
// that is closed stays closed in all of them.

// m7Pages stores a page per access mode over the monitors of feedFixture
// and returns the unlisted token. The public page is also mapped to
// mappedHost.
func (e *appEnv) m7Pages(t *testing.T) (token string) {
	t.Helper()
	db := e.feedFixture(t) // page "main" is replaced below
	e.exec(t, `DELETE FROM status_pages`)
	monitors := []store.StatusPageMonitorInput{{MonitorID: db, DisplayName: "Database"}}
	hash, err := auth.HashPassword(pagePassword)
	if err != nil {
		t.Fatal(err)
	}
	tok, tokHash, _ := statuspage.NewToken()
	e.addPage(t, store.StatusPageInput{Slug: "open", Title: "Open", Monitors: monitors, Hosts: []string{mappedHost}})
	e.addPage(t, store.StatusPageInput{Slug: "partners", Title: "Partners", Visibility: "password", PasswordHash: hash, Monitors: monitors})
	e.addPage(t, store.StatusPageInput{Slug: "internal", Title: "Internal", Visibility: "authenticated", Monitors: monitors})
	e.addPage(t, store.StatusPageInput{Slug: "hidden", Title: "Hidden", Visibility: "unlisted", TokenHash: tokHash, Monitors: monitors})
	return tok
}

func noPrivate(t *testing.T, what, body string) {
	t.Helper()
	for _, leak := range []string{privateName, privateHost, privateIP, "s3cret", privateSummary, privateSnippet, "inc-secret-id", "api-internal"} {
		if strings.Contains(body, leak) {
			t.Errorf("%s leaks %q", what, leak)
		}
	}
}

func TestM7NothingPrivateInAnyFormat(t *testing.T) {
	e := newAppEnv(t)
	token := e.m7Pages(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	session, _ := e.signIn(t, "v1")
	cookie := pageCookieOf(e.unlock("/status/partners", pagePassword, ""))
	if cookie == nil {
		t.Fatal("no page cookie")
	}

	for _, ext := range []string{"", "/api.json", "/feed.xml"} {
		for _, tg := range []struct {
			name, path, host string
			auth             func(*http.Request)
			status           int
		}{
			{"public", "/status/open" + ext, "", nil, 200},
			{"mapped", map[bool]string{true: "/", false: ext}[ext == ""], mappedHost, nil, 200},
			{"unlisted", "/s/" + token + ext, "", nil, 200},
			{"password unlocked", "/status/partners" + ext, "", func(r *http.Request) { r.AddCookie(cookie) }, 200},
			{"authenticated signed in", "/status/internal" + ext, "", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: session}) }, 200},
		} {
			r := req("GET", tg.path, nil)
			if tg.host != "" {
				r = onHost(r, tg.host)
			}
			if tg.auth != nil {
				tg.auth(r)
			}
			rec := e.serve(r)
			if rec.Code != tg.status {
				t.Errorf("%s %q = %d, want %d", tg.name, tg.path, rec.Code, tg.status)
			}
			if !strings.Contains(rec.Body.String(), "Database") && ext != "/feed.xml" {
				t.Errorf("%s %q does not show the public name", tg.name, tg.path)
			}
			noPrivate(t, tg.name+" "+tg.path, rec.Body.String())
		}
	}
}

func TestM7FeedsCannotBypassAccess(t *testing.T) {
	e := newAppEnv(t)
	token := e.m7Pages(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	other, _ := e.signIn(t, "v1")
	good := pageCookieOf(e.unlock("/status/partners", pagePassword, ""))
	e.passwordPage(t, "second") // its cookie must not open "partners"
	second := pageCookieOf(e.unlock("/status/second", pagePassword, ""))

	closed := []string{"/status/partners/api.json", "/status/partners/feed.xml", "/status/internal/api.json", "/status/internal/feed.xml",
		"/status/hidden/api.json", "/status/hidden/feed.xml", "/s/" + token + "x/api.json", "/s/short/feed.xml"}
	forged := &http.Cookie{Name: pageCookie, Value: good.Value[:len(good.Value)-2] + "xx"}
	for _, path := range closed {
		for name, attach := range map[string]func(*http.Request){
			"anonymous":       func(*http.Request) {},
			"another page's":  func(r *http.Request) { r.AddCookie(second) },
			"forged cookie":   func(r *http.Request) { r.AddCookie(forged) },
			"a bearer header": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) },
			"a query token":   func(r *http.Request) { r.URL.RawQuery = "token=" + token + "&password=" + pagePassword },
		} {
			r := req("GET", path, nil)
			attach(r)
			rec := e.serve(r)
			if rec.Code == 200 || strings.Contains(rec.Body.String(), "Database") {
				t.Errorf("%s %s = %d with content", name, path, rec.Code)
			}
			noPrivate(t, name+" "+path, rec.Body.String())
		}
	}
	// The password cookie opens its own page and nothing else; a session
	// opens the authenticated page and does not open the password one.
	r := req("GET", "/status/internal/api.json", nil)
	r.AddCookie(good)
	if rec := e.serve(r); rec.Code != http.StatusUnauthorized {
		t.Errorf("password cookie on an authenticated page = %d", rec.Code)
	}
	r = withCookie(req("GET", "/status/partners/feed.xml", nil), other)
	if rec := e.serve(r); rec.Code != http.StatusUnauthorized {
		t.Errorf("session on a password page = %d", rec.Code)
	}
	// A POST (the password form) is for the HTML page only.
	for _, ext := range []string{"/api.json", "/feed.xml"} {
		if rec := e.unlock("/status/partners"+ext, pagePassword, ""); rec.StatusCode == 200 || pageCookieOf(rec) != nil {
			t.Errorf("POST /status/partners%s = %d, cookie %v", ext, rec.StatusCode, pageCookieOf(rec))
		}
	}
	// A password change locks the machine formats again.
	hash, _ := auth.HashPassword("a new password entirely")
	e.exec(t, `UPDATE status_pages SET password_hash = ? WHERE slug = 'partners'`, hash)
	e.exec(t, `UPDATE status_pages SET updated_at = ? WHERE slug = 'partners'`, store.FormatTime(time.Now().Add(time.Second)))
	r = req("GET", "/status/partners/api.json", nil)
	r.AddCookie(good)
	if rec := e.serve(r); rec.Code != http.StatusUnauthorized {
		t.Errorf("old cookie after a password change = %d", rec.Code)
	}
}

// The admin API and the app are not reachable through a status page's
// hostname, whatever session cookie rides along.
func TestM7MappedHostHasNoAdminSurface(t *testing.T) {
	e := newAppEnv(t)
	e.m7Pages(t)
	e.addUser(t, "a1", "admin", "admin", "")
	session, _ := e.signIn(t, "a1")
	for _, path := range []string{"/api/v1/status", "/api/v1/monitors", "/monitors", "/status/partners/api.json", "/login", "/events"} {
		r := withCookie(onHost(req("GET", path, nil), mappedHost), session)
		if rec := e.serve(r); rec.Code != 404 {
			t.Errorf("GET %s on a mapped host = %d, want 404", path, rec.Code)
		}
	}
	r := withCookie(onHost(req("POST", "/api/v1/monitors/x/pause", nil), mappedHost), session)
	r.Header.Set(APIHeader, apiHeaderValue)
	if rec := e.serve(r); rec.Code != 404 {
		t.Errorf("POST pause on a mapped host = %d", rec.Code)
	}
}
