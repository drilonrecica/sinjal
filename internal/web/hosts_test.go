package web

import (
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
)

const mappedHost = "status.example.com"

func onHost(r *http.Request, host string) *http.Request {
	r.Host = host
	return r
}

// mappedPage adds a public page on mappedHost.
func (e *appEnv) mappedPage(t *testing.T, in store.StatusPageInput) string {
	t.Helper()
	m := e.addMonitor(t, "internal-name", "https://private.corp.internal/")
	in.Hosts = []string{mappedHost}
	in.Monitors = []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Shop"}}
	return e.addPage(t, in)
}

func TestMappedHostServesThePage(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	for _, host := range []string{mappedHost, "Status.Example.COM", mappedHost + ":8443", mappedHost + "."} {
		rec := e.serve(onHost(req("GET", "/", nil), host))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Shop status") {
			t.Errorf("GET / on %q = %d", host, rec.Code)
		}
	}
	// The same page keeps its path address on the instance's own host.
	if rec := e.serve(req("GET", "/status/shop", nil)); rec.Code != 200 {
		t.Errorf("path address = %d", rec.Code)
	}
}

func TestMappedHostServesNothingElse(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	e.addUser(t, "a1", "admin", "admin", "")
	cookie, _ := e.signIn(t, "a1")
	param := regexp.MustCompile(`\{[^}]+\}|\*`)
	err := chi.Walk(e.h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// /events would stream for the admin if it were served; the list
		// below asks for it anonymously.
		if route == "/" || route == "/healthz" || route == "/events" {
			return nil
		}
		path := param.ReplaceAllString(route, "x")
		r := withCookie(onHost(req(method, path, url.Values{}), mappedHost), cookie)
		r.Header.Set("Origin", "http://"+mappedHost)
		if rec := e.serve(r); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on a mapped host = %d, want 404", method, route, rec.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz", "/login", "/setup", "/events", "/api/v1/status", "/status/shop", "/s/x", "/monitors", "/status/shop/api.json", "/s/x/feed.xml"} {
		if rec := e.serve(onHost(req("GET", path, nil), mappedHost)); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s on a mapped host = %d, want 404", path, rec.Code)
		}
	}
	// Not even the signed-in admin's overview at /: the page is there.
	if rec := e.serve(withCookie(onHost(req("GET", "/", nil), mappedHost), cookie)); rec.Code != 200 || strings.Contains(rec.Body.String(), "Sign out") {
		t.Errorf("admin on a mapped host = %d", rec.Code)
	}
}

func TestMappedHostAssets(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	logo := "0123456789abcdef0123456789abcdef.png"
	if err := os.WriteFile(filepath.Join(e.uploads, logo), []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", assets.URL("css/public.css"), "/uploads/" + logo} {
		if rec := e.serve(onHost(req("GET", path, nil), mappedHost)); rec.Code != 200 {
			t.Errorf("GET %s on a mapped host = %d", path, rec.Code)
		}
	}
}

func TestUnmappedHostsServeTheApp(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	// A mapping of the base-URL host (refused by the form) is ignored.
	e.exec(t, `INSERT INTO status_page_hosts (hostname, status_page_id) SELECT 'localhost', id FROM status_pages`)
	for _, host := range []string{"other.example.com", "localhost:8080", "127.0.0.1"} {
		if rec := e.serve(onHost(req("GET", "/login", nil), host)); rec.Code != 200 {
			t.Errorf("GET /login on %s = %d", host, rec.Code)
		}
	}
}

func TestMappedHostNeedsTrustedProxy(t *testing.T) {
	e := newAppEnv(t, netip.MustParsePrefix("10.0.0.0/8"))
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	forwarded := func(peer string) *http.Request {
		r := onHost(req("GET", "/login", nil), "localhost")
		r.RemoteAddr = peer + ":4000"
		r.Header.Set("X-Forwarded-Host", mappedHost)
		return r
	}
	if rec := e.serve(forwarded("192.0.2.7")); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Errorf("spoofed X-Forwarded-Host from an untrusted peer = %d, want the login page", rec.Code)
	}
	if rec := e.serve(forwarded("10.1.2.3")); rec.Code != http.StatusNotFound {
		t.Errorf("X-Forwarded-Host from the trusted proxy: /login = %d, want 404", rec.Code)
	}
	r := forwarded("10.1.2.3")
	r.URL.Path = "/"
	if rec := e.serve(r); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Shop status") {
		t.Errorf("page through the trusted proxy = %d", rec.Code)
	}
}

func TestMappedHostAccessModes(t *testing.T) {
	e := newAppEnv(t)
	hash, _ := auth.HashPassword(pagePassword)
	e.mappedPage(t, store.StatusPageInput{Slug: "p", Title: "Partners", Visibility: "password", PasswordHash: hash})
	if rec := e.serve(onHost(req("GET", "/", nil), mappedHost)); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `action="/"`) {
		t.Fatalf("locked = %d", rec.Code)
	}
	r := onHost(req("POST", "/", url.Values{"password": {pagePassword}}), mappedHost)
	r.Header.Set("Origin", "http://"+mappedHost)
	res := e.serve(r).Result()
	c := pageCookieOf(res)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" || c == nil || c.Path != "/" {
		t.Fatalf("unlock = %d %q cookie %+v", res.StatusCode, res.Header.Get("Location"), c)
	}
	r = onHost(req("GET", "/", nil), mappedHost)
	r.AddCookie(c)
	if rec := e.serve(r); rec.Code != 200 {
		t.Errorf("unlocked = %d", rec.Code)
	}
	r = onHost(req("POST", "/", url.Values{"password": {pagePassword}}), mappedHost)
	r.Header.Set("Origin", "https://evil.example")
	if rec := e.serve(r); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin unlock = %d", rec.Code)
	}
}

func TestMappedHostAuthenticatedPage(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "internal", Title: "Internal", Visibility: "authenticated"})
	rec := e.serve(onHost(req("GET", "/", nil), mappedHost))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != testBaseURL+"/status/internal" {
		t.Errorf("authenticated page on a mapped host = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	bare := newAppEnvAt(t, "")
	bare.mappedPage(t, store.StatusPageInput{Slug: "internal", Title: "Internal", Visibility: "authenticated"})
	rec = bare.serve(onHost(req("GET", "/", nil), mappedHost))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Sign in to Sinjal") || strings.Contains(rec.Body.String(), "Shop") {
		t.Errorf("without a base URL = %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestMappedHostUnlistedPage(t *testing.T) {
	e := newAppEnv(t)
	_, hash, _ := statuspage.NewToken()
	e.mappedPage(t, store.StatusPageInput{Slug: "u", Title: "Unlisted", Visibility: "unlisted", TokenHash: hash})
	rec := e.serve(onHost(req("GET", "/", nil), mappedHost))
	if rec.Code != 200 || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("unlisted on a mapped host = %d %v", rec.Code, rec.Header())
	}
}

func TestRequestHost(t *testing.T) {
	for in, want := range map[string]string{
		"status.example.com":     "status.example.com",
		"STATUS.example.com:443": "status.example.com",
		"status.example.com.":    "status.example.com",
		"status.example.com.:80": "status.example.com",
		"[2001:db8::1]:8080":     "2001:db8::1",
		"[2001:db8::1]":          "2001:db8::1",
		"192.0.2.1:80":           "192.0.2.1",
	} {
		if got := requestHost(onHost(req("GET", "/", nil), in)); got != want {
			t.Errorf("requestHost(%q) = %q, want %q", in, got, want)
		}
	}
}
