package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

var monitorsNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// addMonitor creates an HTTP monitor and returns its id.
func (e *appEnv) addMonitor(t testing.TB, name, url string, tags ...string) string {
	t.Helper()
	id, err := store.CreateMonitor(t.Context(), e.db, store.MonitorInput{
		Name: name, Enabled: true, Tags: tags,
		HTTP: store.HTTPConfig{URL: url, FollowRedirects: true},
	}, monitorsNow)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// getAs requests path with the session of an existing user.
func (e *appEnv) getAs(t *testing.T, userID, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	cookie, _ := e.signIn(t, userID)
	return e.serve(withCookie(req(method, path, nil), cookie))
}

func TestMonitorFragments(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com:8443/path/secret?token=abc", "prod")
	if _, err := e.db.Writer.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, 128.4, 1)`,
		id, store.FormatTime(monitorsNow)); err != nil {
		t.Fatal(err)
	}

	for _, frag := range []string{"row", "header"} {
		path := "/fragments/monitors/" + id + "/" + frag
		t.Run(frag, func(t *testing.T) {
			admin := e.getAs(t, "a1", "GET", path)
			if admin.Code != 200 || admin.Header().Get("Cache-Control") != "no-store" ||
				admin.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("admin = %d %v", admin.Code, admin.Header())
			}
			body := admin.Body.String()
			for _, want := range []string{"API", "https://api.example.com:8443", `data-monitor-id="` + id + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("admin fragment lacks %q:\n%s", want, body)
				}
			}
			// The column row is compact: no latency; the header has it.
			if frag == "header" && !strings.Contains(body, "128 ms") {
				t.Errorf("header lacks its latency:\n%s", body)
			}
			for _, secret := range []string{"secret", "token=abc"} {
				if strings.Contains(body, secret) {
					t.Errorf("fragment leaks %q:\n%s", secret, body)
				}
			}
			// A row's tags are what the column's search matches.
			if frag == "row" && !strings.Contains(body, `data-search="api http https://api.example.com:8443 prod"`) {
				t.Errorf("row lacks its search text:\n%s", body)
			}
			if strings.Contains(body, "<html") {
				t.Error("a fragment must not carry the page layout")
			}

			viewer := e.getAs(t, "v1", "GET", path)
			if viewer.Code != 200 || !strings.Contains(viewer.Body.String(), "API") {
				t.Fatalf("viewer = %d", viewer.Code)
			}
			if strings.Contains(viewer.Body.String(), "example.com") {
				t.Errorf("a viewer sees the checked address:\n%s", viewer.Body.String())
			}
		})
	}

	if rec := e.getAs(t, "a1", "HEAD", "/fragments/monitors/"+id+"/row"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	for _, frag := range []string{"row", "header"} {
		if rec := e.getAs(t, "a1", "GET", "/fragments/monitors/nope/"+frag); rec.Code != http.StatusNotFound {
			t.Errorf("unknown monitor %s = %d, want 404", frag, rec.Code)
		}
	}
	if rec := e.serve(req("GET", "/fragments/monitors/"+id+"/row", nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d, want 303 to login", rec.Code)
	}
}

func TestMonitorFragmentShowsStateAndParent(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	parent := e.addMonitor(t, "Gateway", "https://gw.example.com")
	child := e.addMonitor(t, "App", "https://app.example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET parent_monitor_id = ?, current_state = 'down' WHERE id = ?`, parent, child); err != nil {
		t.Fatal(err)
	}
	// The column row says the state (in words for assistive technology);
	// the header says it and the dependency.
	row := e.getAs(t, "v1", "GET", "/fragments/monitors/"+child+"/row").Body.String()
	for _, want := range []string{`data-state="down"`, `<span class="visually-hidden">Down:</span>`} {
		if !strings.Contains(row, want) {
			t.Errorf("row lacks %q:\n%s", want, row)
		}
	}
	header := e.getAs(t, "v1", "GET", "/fragments/monitors/"+child+"/header").Body.String()
	for _, want := range []string{"Depends on Gateway", `data-state="down"`, ">Down<"} {
		if !strings.Contains(header, want) {
			t.Errorf("header lacks %q:\n%s", want, header)
		}
	}
}

func TestDisplayState(t *testing.T) {
	flap := monitorsNow
	for name, tc := range map[string]struct {
		m    store.Monitor
		want string
	}{
		"up":               {store.Monitor{Enabled: true, State: "up"}, templates.StateUp},
		"down":             {store.Monitor{Enabled: true, State: "down"}, templates.StateDown},
		"pending":          {store.Monitor{Enabled: true, State: "pending"}, templates.StatePending},
		"paused state":     {store.Monitor{Enabled: true, State: "paused"}, templates.StatePaused},
		"disabled wins":    {store.Monitor{State: "up"}, templates.StatePaused},
		"flapping overlay": {store.Monitor{Enabled: true, State: "up", FlappingSince: &flap}, templates.StateFlapping},
		"paused not flap":  {store.Monitor{State: "down", FlappingSince: &flap}, templates.StatePaused},
	} {
		if got := displayState(tc.m); got != tc.want {
			t.Errorf("%s: displayState = %q, want %q", name, got, tc.want)
		}
	}
}

func TestTargetSummary(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/a/b?x=1":  "https://example.com",
		"http://u:p@example.com:8080/": "http://example.com:8080",
		"https://[2001:db8::1]:443/x":  "https://[2001:db8::1]:443",
		"":                             "",
		"example.com":                  "",
		"ftp://example.com":            "",
		"https://":                     "",
		"://bad":                       "",
		"https://example.com/\x7f":     "",
	} {
		if got := targetSummary("http", in); got != want {
			t.Errorf("targetSummary(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct{ typ, raw, want string }{
		{"tcp", "db.internal:5432", "db.internal:5432"},
		{"icmp", "192.0.2.1", "192.0.2.1"},
		{"dns", "example.com MX", "example.com MX"},
		{"heartbeat", "nightly backup", "nightly backup"},
		{"heartbeat", "", "push"},
	} {
		if got := targetSummary(c.typ, c.raw); got != c.want {
			t.Errorf("targetSummary(%s, %q) = %q, want %q", c.typ, c.raw, got, c.want)
		}
	}
}

func TestFormatLatencyAndSince(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "<1 ms", 400 * time.Microsecond: "<1 ms", time.Millisecond: "1 ms", 128400 * time.Microsecond: "128 ms",
		999 * time.Millisecond: "999 ms", time.Second: "1.0 s", 1520 * time.Millisecond: "1.5 s",
	} {
		if got := formatLatency(d); got != want {
			t.Errorf("formatLatency(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		-time.Minute: "0s", 0: "0s", 42 * time.Second: "42s", 5 * time.Minute: "5m", 59*time.Minute + 59*time.Second: "59m",
		time.Hour: "1h", 2*time.Hour + 5*time.Minute: "2h 5m", 24 * time.Hour: "1d", 3*24*time.Hour + 4*time.Hour + 30*time.Minute: "3d 4h",
	} {
		if got := formatSince(d); got != want {
			t.Errorf("formatSince(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestMonitorViewDefaults(t *testing.T) {
	m := store.Monitor{ID: "m", Name: "n", Type: "http", Enabled: true, State: "pending", StateSince: monitorsNow}
	v := monitorView(m, viewInput{Admin: true, Target: "https://x.example"}, monitorsNow.Add(90*time.Second))
	if v.Latency != "—" || v.LastCheck != "never" || v.Uptime != "—" || v.Since != "1m" || v.Target != "https://x.example" {
		t.Errorf("view = %+v", v)
	}
	last := monitorsNow.Add(30 * time.Second)
	m.LastCheckAt = &last
	if v := monitorView(m, viewInput{}, monitorsNow.Add(90*time.Second)); v.LastCheck != "1m ago" || v.Target != "" {
		t.Errorf("view = %+v", v)
	}
}

// The shell's monitor column (docs/03 "Layout"): compact rows in name
// order, each a link with its state, name, 24-hour uptime and beat bar;
// targets only in an admin's search text, never a path or query.
func TestMonitorColumn(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	a := e.addMonitor(t, "Beta", "https://beta.example.com/health?key=s3cret", "prod", "eu")
	e.addMonitor(t, "alpha", "https://alpha.example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}

	admin := e.getAs(t, "a1", "GET", "/fragments/monitors")
	body := admin.Body.String()
	if admin.Code != 200 || admin.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "<html") {
		t.Fatalf("admin = %d", admin.Code)
	}
	for _, want := range []string{
		`data-live-list hx-get="/fragments/monitors"`, `data-search="beta http https://beta.example.com eu prod"`,
		`data-search="alpha http https://alpha.example.com"`, `<span class="visually-hidden">Down:</span>`,
		`<span class="visually-hidden">Pending:</span>`, `<span class="column-name">Beta</span>`,
		`title="Uptime, last 24 hours">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin column lacks %q", want)
		}
	}
	if strings.Contains(body, "s3cret") || strings.Contains(body, "/health") {
		t.Error("the column leaks the path or query of a checked address")
	}
	if strings.Index(body, ">alpha</span>") > strings.Index(body, ">Beta</span>") {
		t.Error("monitors are not ordered by name, case-insensitively")
	}
	if n := strings.Count(body, `class="column-row"`); n != 2 {
		t.Errorf("%d rows, want 2", n)
	}

	viewer := e.getAs(t, "v1", "GET", "/fragments/monitors").Body.String()
	if !strings.Contains(viewer, ">Beta</span>") || strings.Contains(viewer, "example.com") {
		t.Error("a viewer must see the monitors but not their addresses")
	}
	if rec := e.getAs(t, "v1", "HEAD", "/fragments/monitors"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

// The column's rows show the 24-hour raw uptime, the detail header's raw
// figure, read for all monitors at once.
func TestMonitorColumnUptime(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, store.FormatTime(now.Add(-48*time.Hour)), id); err != nil {
		t.Fatal(err)
	}
	e.addIncident(t, "i1", id, now.Add(-6*time.Hour), ptrTime(now.Add(-3*time.Hour)), false)
	header := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/header").Body.String()
	column := e.getAs(t, "v1", "GET", "/fragments/monitors").Body.String()
	row := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/row").Body.String()
	for name, body := range map[string]string{"header": header, "column": column, "row": row} {
		if !strings.Contains(body, "87.50%") {
			t.Errorf("%s does not show 87.50%% (3 h down of 24 h):\n%s", name, body)
		}
	}
}

// /monitors is the list view: Home in the pane, marked so a phone shows
// the monitor column instead (docs/03 "Phones").
func TestMonitorListView(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	e.addMonitor(t, "API", "https://api.example.com")
	body := e.getAs(t, "v1", "GET", "/monitors").Body.String()
	for _, want := range []string{`<main id="main" class="content" tabindex="-1" data-view="list" hx-history-elt>`, `class="home-hero"`, "<title>Monitors — Sinjal</title>"} {
		if !strings.Contains(body, want) {
			t.Errorf("list view lacks %q", want)
		}
	}
	if home := e.getAs(t, "v1", "GET", "/").Body.String(); !strings.Contains(home, `data-view="detail"`) {
		t.Error("Home is not the detail view")
	}
	if rec := e.getAs(t, "v1", "HEAD", "/monitors"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestMonitorColumnEmpty(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	admin := e.getAs(t, "a1", "GET", "/fragments/monitors").Body.String()
	if !strings.Contains(admin, "No monitors yet.") || !strings.Contains(admin, `href="/monitors/new"`) {
		t.Errorf("admin empty column:\n%s", admin)
	}
	viewer := e.getAs(t, "v1", "GET", "/fragments/monitors").Body.String()
	if !strings.Contains(viewer, "No monitors yet.") || strings.Contains(viewer, "/monitors/new") {
		t.Errorf("viewer empty column:\n%s", viewer)
	}
}

func TestMonitorListRequiresSignIn(t *testing.T) {
	e := newAppEnv(t)
	if rec := e.serve(req("GET", "/monitors", nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d, want 303 to login", rec.Code)
	}
}

// A monitor whose parent is down says so on its page (the column row is
// compact: state, name, uptime, beats).
func TestParentDownLabel(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	c := e.addMonitor(t, "child", "https://child.example.com")
	p := e.addMonitor(t, "parent", "https://parent.example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET parent_monitor_id = ? WHERE id = ?`, p, c); err != nil {
		t.Fatal(err)
	}
	const label = "Parent down: notifications held"
	if body := e.getAs(t, "v1", "GET", "/monitors/"+c).Body.String(); strings.Contains(body, label) {
		t.Fatal("the label is shown while the parent is pending")
	}
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/monitors/" + c, "/fragments/monitors/" + c + "/header"} {
		if body := e.getAs(t, "v1", "GET", path).Body.String(); strings.Count(body, label) != 1 {
			t.Errorf("%s: the label is shown %d times, want once", path, strings.Count(body, label))
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
