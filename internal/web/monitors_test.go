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
	id, err := store.CreateHTTPMonitor(t.Context(), e.db, store.HTTPMonitor{
		Name: name, Enabled: true, Tags: tags,
		Config: store.HTTPConfig{URL: url, FollowRedirects: true},
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
		if got := targetSummary(in); got != want {
			t.Errorf("targetSummary(%q) = %q, want %q", in, got, want)
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
	v := monitorView(m, viewInput{Admin: true, URL: "https://x.example"}, monitorsNow.Add(90*time.Second))
	if v.Latency != "—" || v.LastCheck != "never" || v.Uptime != "—" || v.Since != "1m" || v.Target != "https://x.example" {
		t.Errorf("view = %+v", v)
	}
	last := monitorsNow.Add(30 * time.Second)
	m.LastCheckAt = &last
	if v := monitorView(m, viewInput{}, monitorsNow.Add(90*time.Second)); v.LastCheck != "1m ago" || v.Target != "" {
		t.Errorf("view = %+v", v)
	}
}
