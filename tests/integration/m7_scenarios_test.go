package integration

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M7-10: scenarios 17 and 19 of docs/20 for status pages, against the
// binary: nothing private reaches a visitor in any format or access mode,
// the feeds cannot get around a page's access rules, and a custom hostname
// is believed only when the proxy is trusted. The same checks run in
// process in internal/web (publicfeed_test.go, hosts_test.go, m7_test.go).

const (
	m7Host     = "status.example.com"
	m7Private  = "private-db.corp.internal"
	m7Secret   = "s3cr3t-query-token"
	m7Summary  = "dial tcp 10.20.30.40:5432: connection refused"
	m7Snippet  = "internal stack trace corp.internal"
	m7Internal = "Primary database (internal)"
	m7Password = "correct horse battery"
)

// m7Seed stores four pages over one monitor, with an open incident and a
// failed check carrying private values, in a stopped server's database,
// and returns the unlisted page's token.
func m7Seed(t *testing.T, dataDir string) (token string) {
	t.Helper()
	d, err := db.Open(filepath.Join(dataDir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, now := context.Background(), time.Now()
	mon, err := store.CreateMonitor(ctx, d, store.MonitorInput{Name: m7Internal,
		HTTP: store.HTTPConfig{URL: "https://" + m7Private + ":5432/health?token=" + m7Secret}}, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success, error_kind, error_message, diagnostic_snippet)
			VALUES ('` + mon + `', '` + store.FormatTime(now) + `', 812, 0, 'connect', '` + m7Summary + `', '` + m7Snippet + `')`,
		`INSERT INTO incidents (id, monitor_id, started_at, initial_failure_kind, summary, suppressed_by_parent, created_at)
			VALUES ('inc-internal-id', '` + mon + `', '` + store.FormatTime(now.Add(-time.Hour)) + `', 'connect', '` + m7Summary + `', 0, '` + store.FormatTime(now) + `')`,
	} {
		if _, err := d.Writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddIncidentNote(ctx, d, "inc-internal-id", "We are restoring the database.", true, now); err != nil {
		t.Fatal(err)
	}
	pwHash, err := auth.HashPassword(m7Password)
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash, err := statuspage.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	monitors := []store.StatusPageMonitorInput{{MonitorID: mon, DisplayName: "Database", ShowLatency: true}}
	for _, p := range []store.StatusPageInput{
		{Slug: "open", Title: "Open page", Visibility: "public", Hosts: []string{m7Host}},
		{Slug: "partners", Title: "Partners page", Visibility: "password", PasswordHash: pwHash},
		{Slug: "internal", Title: "Internal page", Visibility: "authenticated"},
		{Slug: "hidden", Title: "Hidden page", Visibility: "unlisted", TokenHash: tokenHash},
	} {
		p.Theme, p.IncidentDays, p.Monitors = "paper", 30, monitors
		if _, err := store.CreateStatusPage(ctx, d, p, now); err != nil {
			t.Fatal(err)
		}
	}
	return token
}

// m7Server seeds a data directory and serves it with env.
func m7Server(t *testing.T, env ...string) (*server, string) {
	t.Helper()
	skipShort(t)
	dir := filepath.Join(t.TempDir(), "data")
	first := start(t, dir)
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}
	token := m7Seed(t, dir)
	return start(t, dir, env...), token
}

// m7Get sends one request without following redirects. host sets the Host
// header; hdr adds headers.
func m7Get(t *testing.T, s *server, path, host string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

var m7Leaks = []string{m7Private, m7Secret, "10.20.30.40", m7Summary, m7Snippet, m7Internal, "inc-internal-id", "Primary database"}

func m7NoLeaks(t *testing.T, what, body string) {
	t.Helper()
	for _, leak := range m7Leaks {
		if strings.Contains(body, leak) {
			t.Errorf("%s leaks %q", what, leak)
		}
	}
}

// Scenario 17 and the access rules: every format of every access mode
// shows the public name and nothing private, and feeds never open a page
// that is closed.
func TestStatusPagesRedactionAndAccess(t *testing.T) {
	s, token := m7Server(t)
	for _, c := range []struct {
		path   string
		status int
		shows  string // "" for a refusal
		header string // a header the response must carry, "Name: value"
	}{
		{"/status/open", 200, "Database", ""},
		{"/status/open/api.json", 200, `"name":"Database"`, "Cache-Control: no-store"},
		{"/status/open/feed.xml", 200, "Database: ongoing", "Cache-Control: no-store"},
		{"/s/" + token, 200, "Database", "X-Robots-Tag: noindex, nofollow"},
		{"/s/" + token + "/api.json", 200, `"name":"Database"`, "Referrer-Policy: no-referrer"},
		{"/s/" + token + "/feed.xml", 200, "Database: ongoing", "X-Robots-Tag: noindex, nofollow"},
		// An unlisted page has no slug address.
		{"/status/hidden", 404, "", ""},
		{"/status/hidden/api.json", 404, "", ""},
		{"/status/hidden/feed.xml", 404, "", ""},
		// Password and authenticated pages: the machine formats refuse
		// instead of showing a form or redirecting.
		{"/status/partners", 401, "", ""},
		{"/status/partners/api.json", 401, "", ""},
		{"/status/partners/feed.xml", 401, "", ""},
		{"/status/internal", 303, "", ""},
		{"/status/internal/api.json", 401, "", ""},
		{"/status/internal/feed.xml", 401, "", ""},
	} {
		resp, body := m7Get(t, s, c.path, "", nil)
		name := strings.Replace(c.path, token, "<token>", 1)
		if resp.StatusCode != c.status {
			t.Errorf("GET %s = %d, want %d", name, resp.StatusCode, c.status)
		}
		if c.shows != "" && !strings.Contains(body, c.shows) {
			t.Errorf("GET %s lacks %q", name, c.shows)
		}
		if c.header != "" {
			k, v, _ := strings.Cut(c.header, ": ")
			if got := resp.Header.Get(k); got != v {
				t.Errorf("GET %s %s = %q, want %q", name, k, got, v)
			}
		}
		m7NoLeaks(t, "GET "+name, body)
		if c.shows == "" && (strings.Contains(body, "Database") || strings.Contains(body, "restoring")) {
			t.Errorf("GET %s refused but shows the page's content", name)
		}
	}
}

// Scenario 19 for hostnames: a mapped hostname is believed from the Host
// header, and from X-Forwarded-Host only when the peer is a trusted proxy.
func TestStatusPageHostRouting(t *testing.T) {
	t.Run("untrusted peer", func(t *testing.T) {
		s, _ := m7Server(t, "SINJAL_TRUSTED_PROXIES=10.0.0.0/8")
		spoof := map[string]string{"X-Forwarded-Host": m7Host}
		for _, path := range []string{"/", "/api.json", "/feed.xml"} {
			resp, body := m7Get(t, s, path, "", spoof)
			if resp.StatusCode == 200 && strings.Contains(body, "Open page") {
				t.Errorf("a spoofed X-Forwarded-Host served the page at %s", path)
			}
			m7NoLeaks(t, "spoofed "+path, body)
		}
		// The app answers there as usual: the overview wants a sign-in.
		if resp, _ := m7Get(t, s, "/", "", spoof); resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
			t.Errorf("/ with a spoofed X-Forwarded-Host = %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		// The Host header itself is the peer's own claim about where it
		// connected: it serves the page.
		if resp, body := m7Get(t, s, "/", m7Host, nil); resp.StatusCode != 200 || !strings.Contains(body, "Open page") {
			t.Errorf("Host %s = %d", m7Host, resp.StatusCode)
		}
	})
	t.Run("trusted proxy", func(t *testing.T) {
		s, _ := m7Server(t, "SINJAL_TRUSTED_PROXIES=127.0.0.1")
		fwd := map[string]string{"X-Forwarded-Host": m7Host}
		for path, want := range map[string]string{"/": "Open page", "/api.json": `"title":"Open page"`, "/feed.xml": "Open page incidents"} {
			resp, body := m7Get(t, s, path, "", fwd)
			if resp.StatusCode != 200 || !strings.Contains(body, want) {
				t.Errorf("%s through the proxy = %d", path, resp.StatusCode)
			}
			m7NoLeaks(t, "mapped "+path, body)
		}
		// Only the page is there: no login, no admin API, no other page.
		for _, path := range []string{"/login", "/setup", "/monitors", "/api/v1/status", "/status/partners", "/events", "/readyz"} {
			if resp, _ := m7Get(t, s, path, "", fwd); resp.StatusCode != 404 {
				t.Errorf("%s on the mapped host = %d, want 404", path, resp.StatusCode)
			}
		}
		// An unmapped forwarded host is the app.
		if resp, _ := m7Get(t, s, "/", "", map[string]string{"X-Forwarded-Host": "other.example.com"}); resp.StatusCode != 303 {
			t.Errorf("unmapped forwarded host = %d", resp.StatusCode)
		}
	})
}
