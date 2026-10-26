package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
)

// SL-08: the four layouts in each theme keep the rules of M7-10. Nothing
// private reaches any format at any address (scenario 17 of docs/20), a
// closed page stays closed, and the page runs no script.

// inlineCode is what the public CSP would block besides an external
// script: any script element, inline styles other than the accent's
// hashed element, event handler attributes and javascript: URLs.
var inlineCode = regexp.MustCompile(`(?i)<script\b|\sstyle=|\son[a-z]+=|javascript:`)

// layoutPages is m7Pages with the private failure (its summary and
// diagnostic snippet) and a day of latency behind the down service, which
// every page shows with its latency, so Heartline draws its pulse.
func (e *appEnv) layoutPages(t *testing.T) (token string) {
	t.Helper()
	token = e.m7Pages(t)
	var db string
	if err := e.db.Reader.QueryRow(`SELECT id FROM monitors WHERE name = ?`, privateName).Scan(&db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 1; i <= 24; i++ {
		e.exec(t, `INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, ?, 1)`, db, store.FormatTime(now.Add(-time.Duration(i)*time.Hour)), 40+i)
	}
	e.addFailure(t, db, now.Add(-time.Minute), "connect", privateSummary, privateSnippet)
	e.exec(t, `UPDATE status_page_monitors SET show_latency = 1`)
	return token
}

func TestLayoutsNothingPrivateAndClosedStaysClosed(t *testing.T) {
	e := newAppEnv(t)
	token := e.layoutPages(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	session, _ := e.signIn(t, "v1")
	cookie := pageCookieOf(e.unlock("/status/partners", pagePassword, ""))
	if cookie == nil {
		t.Fatal("no page cookie")
	}
	sessionCookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: session}) }
	pageCookie := func(r *http.Request) { r.AddCookie(cookie) }

	for i, layout := range statuspage.Layouts {
		for j, theme := range statuspage.Themes {
			// A new updated_at drops the cached view, as a save does.
			e.exec(t, `UPDATE status_pages SET layout = ?, theme = ?, accent = '#3e67a8', updated_at = ?`,
				layout, theme, store.FormatTime(time.Now().Add(time.Duration(i*10+j+1)*time.Second)))
			t.Run(layout+"/"+theme, func(t *testing.T) {
				for _, ext := range []string{"", "/api.json", "/feed.xml"} {
					for _, tg := range []struct {
						name, path, host string
						auth             func(*http.Request)
					}{
						{"public", "/status/open" + ext, "", nil},
						{"mapped", map[bool]string{true: "/", false: ext}[ext == ""], mappedHost, nil},
						{"unlisted", "/s/" + token + ext, "", nil},
						{"password unlocked", "/status/partners" + ext, "", pageCookie},
						{"authenticated signed in", "/status/internal" + ext, "", sessionCookie},
					} {
						r := req("GET", tg.path, nil)
						if tg.host != "" {
							r = onHost(r, tg.host)
						}
						if tg.auth != nil {
							tg.auth(r)
						}
						rec := e.serve(r)
						body := rec.Body.String()
						if rec.Code != 200 {
							t.Fatalf("%s %q = %d", tg.name, tg.path, rec.Code)
						}
						noPrivate(t, tg.name+" "+tg.path, body)
						for _, leak := range []string{"connection refused", "corp.internal", "/monitors/", "/incidents/"} {
							if strings.Contains(body, leak) {
								t.Errorf("%s %s leaks %q", tg.name, tg.path, leak)
							}
						}
						if ext != "" {
							continue
						}
						if !strings.Contains(body, `data-layout="`+layout+`"`) || !strings.Contains(body, `data-theme="`+theme+`"`) {
							t.Fatalf("%s %s is not in %s/%s", tg.name, tg.path, layout, theme)
						}
						if layout == "heartline" && !strings.Contains(body, `<div class="pulse">`) {
							t.Errorf("%s %s draws no pulse", tg.name, tg.path)
						}
						// The CSP is the app's, admitting the accent's style
						// by hash and nothing else; the page needs nothing more.
						csp := rec.Header().Get("Content-Security-Policy")
						style := regexp.MustCompile(`<style>([^<]*)</style>`).FindAllStringSubmatch(body, -1)
						if len(style) != 1 || csp != middleware.CSPWithStyleHash(styleHash(style[0][1])) {
							t.Errorf("%s %s: CSP %q for styles %q", tg.name, tg.path, csp, style)
						}
						if bad := inlineCode.FindString(body); bad != "" {
							t.Errorf("%s %s has %q, which the CSP blocks", tg.name, tg.path, bad)
						}
					}
				}

				// Closed pages stay closed in every format.
				closed := []struct {
					path string
					auth func(*http.Request)
				}{
					{"/status/partners", nil}, {"/status/partners/api.json", nil}, {"/status/partners/feed.xml", nil},
					{"/status/internal", nil}, {"/status/internal/api.json", nil}, {"/status/internal/feed.xml", pageCookie},
					{"/status/partners/api.json", sessionCookie},
					{"/status/hidden", nil}, {"/status/hidden/api.json", nil}, {"/s/" + token + "x", nil}, {"/s/short/feed.xml", nil},
				}
				for _, c := range closed {
					r := req("GET", c.path, nil)
					if c.auth != nil {
						c.auth(r)
					}
					rec := e.serve(r)
					if rec.Code == 200 || strings.Contains(rec.Body.String(), "Database") {
						t.Errorf("closed %s = %d with content", c.path, rec.Code)
					}
					noPrivate(t, "closed "+c.path, rec.Body.String())
				}
			})
		}
	}
}
