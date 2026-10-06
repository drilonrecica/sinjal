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
func (e *appEnv) addMonitor(t *testing.T, name, url string, tags ...string) string {
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
			for _, want := range []string{"API", "128 ms", "https://api.example.com:8443", `data-monitor-id="` + id + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("admin fragment lacks %q:\n%s", want, body)
				}
			}
			for _, secret := range []string{"secret", "token=abc"} {
				if strings.Contains(body, secret) {
					t.Errorf("fragment leaks %q:\n%s", secret, body)
				}
			}
			if frag == "row" && !strings.Contains(body, ">prod<") {
				t.Errorf("row lacks its tag:\n%s", body)
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
	body := e.getAs(t, "v1", "GET", "/fragments/monitors/"+child+"/row").Body.String()
	for _, want := range []string{"Depends on Gateway", `data-state="down"`, ">Down<"} {
		if !strings.Contains(body, want) {
			t.Errorf("row lacks %q:\n%s", want, body)
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

func TestMonitorListPage(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	a := e.addMonitor(t, "Beta", "https://beta.example.com/health?key=s3cret", "prod", "eu")
	e.addMonitor(t, "alpha", "https://alpha.example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET parent_monitor_id = (SELECT id FROM monitors WHERE name = 'alpha'),
		current_state = 'down' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}

	admin := e.getAs(t, "a1", "GET", "/monitors")
	body := admin.Body.String()
	if admin.Code != 200 || admin.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin = %d", admin.Code)
	}
	for _, want := range []string{
		"<h1>Monitors</h1>", `aria-current="page"`, `href="/monitors/new"`,
		`hx-ext="sse"`, `sse-connect="/events"`,
		"https://beta.example.com", "https://alpha.example.com", ">prod</li>", ">eu</li>",
		"Depends on alpha", ">Down<", `>Pending<`, "—",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page lacks %q", want)
		}
	}
	if strings.Contains(body, "s3cret") || strings.Contains(body, "/health") {
		t.Error("the page leaks the path or query of a checked address")
	}
	if strings.Index(body, ">alpha</a>") > strings.Index(body, ">Beta</a>") {
		t.Error("monitors are not ordered by name, case-insensitively")
	}
	if n := strings.Count(body, `class="monitor-row"`); n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
	for _, js := range []string{`/static/js/htmx-ext-sse.`, `/static/js/live.`, `/static/css/monitors.`} {
		if !strings.Contains(body, js) {
			t.Errorf("page does not load %s", js)
		}
	}
	if strings.Index(body, "/static/js/htmx.min.") > strings.Index(body, "/static/js/htmx-ext-sse.") {
		t.Error("the SSE extension loads before htmx, which it needs")
	}

	viewer := e.getAs(t, "v1", "GET", "/monitors").Body.String()
	if !strings.Contains(viewer, ">Beta</a>") || strings.Contains(viewer, "example.com") {
		t.Error("a viewer must see the monitors but not their addresses")
	}
	if strings.Contains(viewer, "/monitors/new") {
		t.Error("a viewer is offered the create action")
	}
	if rec := e.getAs(t, "v1", "HEAD", "/monitors"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestMonitorListEmptyState(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")

	admin := e.getAs(t, "a1", "GET", "/monitors").Body.String()
	for _, want := range []string{"No monitors yet", `<a class="action" href="/monitors/new">Create monitor</a>`} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin empty state lacks %q", want)
		}
	}
	if strings.Count(admin, "Create monitor") != 1 || strings.Contains(admin, "monitor-list") || strings.Contains(admin, "sse-connect") {
		t.Error("the empty state must offer one create action and open no event stream")
	}

	viewer := e.getAs(t, "v1", "GET", "/monitors").Body.String()
	if !strings.Contains(viewer, "No monitors yet") || strings.Contains(viewer, "Create monitor") {
		t.Error("a viewer's empty state must explain without offering to create")
	}
}

func TestMonitorListRequiresSignIn(t *testing.T) {
	e := newAppEnv(t)
	if rec := e.serve(req("GET", "/monitors", nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d, want 303 to login", rec.Code)
	}
}

// A monitor whose parent is down says so on the list and on its page.
func TestParentDownLabel(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	c := e.addMonitor(t, "child", "https://child.example.com")
	p := e.addMonitor(t, "parent", "https://parent.example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET parent_monitor_id = ? WHERE id = ?`, p, c); err != nil {
		t.Fatal(err)
	}
	const label = "Parent down: notifications held"
	if body := e.getAs(t, "v1", "GET", "/monitors").Body.String(); strings.Contains(body, label) {
		t.Fatal("the label is shown while the parent is pending")
	}
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/monitors", "/monitors/" + c, "/fragments/monitors/" + c + "/row"} {
		if body := e.getAs(t, "v1", "GET", path).Body.String(); strings.Count(body, label) != 1 {
			t.Errorf("%s: the label is shown %d times, want once", path, strings.Count(body, label))
		}
	}
}
