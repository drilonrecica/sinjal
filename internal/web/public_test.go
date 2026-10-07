package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// addPage stores a page straight in the store; monitors are shown in the
// order given.
func (e *appEnv) addPage(t *testing.T, in store.StatusPageInput) string {
	t.Helper()
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	if in.Theme == "" {
		in.Theme = "paper"
	}
	if in.IncidentDays == 0 {
		in.IncidentDays = 30
	}
	id, err := store.CreateStatusPage(context.Background(), e.db, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func quietLoggerOnly() *slog.Logger {
	l, _ := quietLogger()
	return l
}

func (e *appEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.db.Writer.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// Distinctive private details that must never reach a public page
// (scenario 17 of docs/20).
const (
	privateName    = "prod-db-primary-07"
	privateHost    = "db-primary.corp.internal"
	privateIP      = "10.20.30.40"
	privateSummary = "dial tcp 10.20.30.40:5432: connection refused"
	privateSnippet = "<html>internal stack trace corp.internal</html>"
)

func TestPublicPageRenders(t *testing.T) {
	e := newAppEnv(t)
	db := e.addMonitor(t, privateName, "https://"+privateHost+":5432/health?token=s3cret")
	api := e.addMonitor(t, "api-internal", "http://"+privateIP+"/healthz")
	site := e.addMonitor(t, "site-internal", "https://www.example.com/")
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	e.exec(t, `UPDATE monitors SET current_state = 'down' WHERE id = ?`, db)
	e.exec(t, `INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, 87, 1)`, api, store.FormatTime(time.Now()))
	e.addFailure(t, db, time.Now().Add(-time.Minute), "connect", privateSummary, privateSnippet)
	e.addIncident(t, "inc1", db, time.Now().Add(-time.Hour), nil, false)
	e.exec(t, `UPDATE incidents SET summary = ? WHERE id = 'inc1'`, privateSummary)
	if _, err := store.AddIncidentNote(context.Background(), e.db, "inc1", "We are restoring the database.", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddIncidentNote(context.Background(), e.db, "inc1", "ssh into "+privateHost, false, time.Now()); err != nil {
		t.Fatal(err)
	}

	e.addPage(t, store.StatusPageInput{Slug: "main", Title: "Acme status", Description: "Everything we run", Accent: "#3e67a8", ShowPoweredBy: true,
		Groups: []string{"Web", "Data"},
		Monitors: []store.StatusPageMonitorInput{
			{MonitorID: site, Group: "Web", DisplayName: "Website", Sort: 2},
			{MonitorID: api, Group: "Web", DisplayName: "Public API", ShowLatency: true, Sort: 1},
			{MonitorID: db, DisplayName: "Database", Sort: 3},
		}})

	rec := e.serve(req("GET", "/status/main", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /status/main = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Acme status", "Everything we run", `data-theme="paper"`, "Partial outage", "Web",
		"We are restoring the database.", "Ongoing", "87 ms", "Powered by", "uptime over 90 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if i, j, k := strings.Index(body, "Public API"), strings.Index(body, "Website"), strings.Index(body, ">Database<"); i < 0 || j < i || k < j {
		t.Errorf("services out of order: API %d, Website %d, Database %d", i, j, k)
	}
	if n := strings.Count(body, `class="public-day `); n != 3*90 {
		t.Errorf("%d strip days, want 90 per service", n)
	}
	if strings.Count(body, " ms") != 1 {
		t.Error("latency shown for a service without show_latency")
	}
	for _, secret := range []string{privateName, privateHost, privateIP, "connection refused", "s3cret", "corp.internal",
		"api-internal", "site-internal", db, api, site, "inc1", "timeout", "/monitors/", "/incidents/"} {
		if strings.Contains(body, secret) {
			t.Errorf("public page leaks %q", secret)
		}
	}

	// The accent is the only inline style, admitted by its hash.
	style := regexp.MustCompile(`<style>([^<]*)</style>`).FindAllStringSubmatch(body, -1)
	if len(style) != 1 || !strings.Contains(style[0][1], "--accent:#3e67a8") {
		t.Fatalf("accent style = %q", style)
	}
	sum := sha256.Sum256([]byte(style[0][1]))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self' 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"';") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec := e.serve(req("HEAD", "/status/main", nil)); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestPublicPageOptions(t *testing.T) {
	e := newAppEnv(t)
	m := e.addMonitor(t, "x", "https://x.example.com/")
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	e.addPage(t, store.StatusPageInput{Slug: "plain", Title: "Plain", Theme: "midnight",
		Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	rec := e.serve(req("GET", "/status/plain", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `data-theme="midnight"`) || !strings.Contains(body, "All systems operational") {
		t.Fatalf("page = %d\n%s", rec.Code, body)
	}
	if strings.Contains(body, "Powered by") || strings.Contains(body, "<style") || strings.Contains(body, "<script") {
		t.Error("footer, inline style or script on a plain page")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sha256-") {
		t.Errorf("CSP without an accent = %q", csp)
	}
	if !strings.Contains(body, "No incidents in the last 30 days") {
		t.Error("no quiet incident text")
	}
}

func TestPublicPageNotServed(t *testing.T) {
	e := newAppEnv(t)
	e.addPage(t, store.StatusPageInput{Slug: "auth", Title: "A", Visibility: "authenticated"})
	e.addPage(t, store.StatusPageInput{Slug: "secret", Title: "S", Visibility: "unlisted", TokenHash: []byte("0123456789abcdef0123456789abcdef")})
	for _, path := range []string{"/status/nope", "/status/secret", "/status/-bad-", "/status/auth"} {
		if rec := e.serve(req("GET", path, nil)); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestPublicIncidentDays(t *testing.T) {
	e := newAppEnv(t)
	m := e.addMonitor(t, "x", "https://x.example.com/")
	ended := time.Now().AddDate(0, 0, -40)
	e.addIncident(t, "old", m, ended.Add(-time.Hour), &ended, false)
	e.addPage(t, store.StatusPageInput{Slug: "short", Title: "Short", Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	e.addPage(t, store.StatusPageInput{Slug: "long", Title: "Long", IncidentDays: 60, Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	if body := e.serve(req("GET", "/status/short", nil)).Body.String(); strings.Contains(body, "Resolved") {
		t.Error("an incident older than incident_days is listed")
	}
	if body := e.serve(req("GET", "/status/long", nil)).Body.String(); !strings.Contains(body, "Resolved") || !strings.Contains(body, "down for 1h") {
		t.Error("an incident within incident_days is missing")
	}
}

func TestPublicStrip(t *testing.T) {
	e := newAppEnv(t)
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, loc) }
	now := day(30).Add(10 * time.Hour) // the day after the switch to summer time
	m, err := store.CreateMonitor(ctx, e.db, store.MonitorInput{Name: "x", Enabled: true, HTTP: store.HTTPConfig{URL: "https://x.example.com/"}}, day(20))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMaintenance(ctx, e.db, maintenance.Window{Name: "w", Start: day(25), Duration: 24 * time.Hour,
		Recurrence: maintenance.None, ExcludeUptime: true, Scope: maintenance.Scope{Monitors: []string{m}}}, day(20)); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at, resumed_at) VALUES (?, ?, ?)`, m, store.FormatTime(day(26)), store.FormatTime(day(27)))
	twoHours, tenMinutes := day(27).Add(2*time.Hour), day(28).Add(10*time.Minute)
	e.addIncident(t, "i1", m, day(27), &twoHours, false)
	e.addIncident(t, "i2", m, day(28), &tenMinutes, false)
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	pageID := e.addPage(t, store.StatusPageInput{Slug: "s", Title: "S", Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	p, err := store.GetStatusPage(ctx, e.db.Reader, pageID)
	if err != nil {
		t.Fatal(err)
	}

	h := NewPublic(e.db, loc, quietLoggerOnly())
	v, err := h.build(ctx, p, now)
	if err != nil {
		t.Fatal(err)
	}
	days := v.Groups[0].Rows[0].Days
	if len(days) != 90 {
		t.Fatalf("%d days", len(days))
	}
	// Index 89 is today, 30 March.
	want := map[int]string{89: templates.DayUp, 88: templates.DayUp, 87: templates.DayPartial, 86: templates.DayDown,
		85: templates.DayNoData, 84: templates.DayMaintenance, 79: templates.DayUp, 78: templates.DayNoData, 0: templates.DayNoData}
	for i, class := range want {
		if days[i].Class != class {
			t.Errorf("day %d (%s) = %s, want %s", i, days[i].Label, days[i].Class, class)
		}
	}
	for i, label := range map[int]string{87: "Sat 28 Mar 2026: 99.30% uptime, 1 incident", 86: "Fri 27 Mar 2026: 91.66% uptime, 1 incident",
		85: "Thu 26 Mar 2026: no data", 84: "Wed 25 Mar 2026: maintenance, no incidents", 88: "Sun 29 Mar 2026: 100.00% uptime, no incidents"} {
		if days[i].Label != label {
			t.Errorf("day %d label = %q, want %q", i, days[i].Label, label)
		}
	}
	if !strings.HasPrefix(v.Groups[0].Rows[0].StripText, "Last 90 days: ") {
		t.Errorf("strip text = %q", v.Groups[0].Rows[0].StripText)
	}
}

func TestPublicStripBoundsAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	b := stripBounds(time.Date(2026, 3, 30, 10, 0, 0, 0, loc), loc)
	if len(b) != 91 {
		t.Fatalf("%d bounds", len(b))
	}
	if got := b[89].Sub(b[88]); got != 23*time.Hour {
		t.Errorf("29 March lasts %v, want 23h", got)
	}
	for _, t0 := range b {
		if t0.In(loc).Hour() != 0 {
			t.Errorf("bound %v is not a local midnight", t0)
		}
	}
}

func TestPublicMaintenanceState(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	m := e.addMonitor(t, "x", "https://x.example.com/")
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	if _, err := store.CreateMaintenance(ctx, e.db, maintenance.Window{Name: "w", Start: time.Now().Add(-time.Hour), Duration: 2 * time.Hour,
		Recurrence: maintenance.None, Scope: maintenance.Scope{Monitors: []string{m}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.addPage(t, store.StatusPageInput{Slug: "m", Title: "M", Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	body := e.serve(req("GET", "/status/m", nil)).Body.String()
	if !strings.Contains(body, "Under maintenance") || !strings.Contains(body, `data-state="maintenance"`) {
		t.Errorf("maintenance not shown:\n%s", body)
	}
}

func TestPublicOverall(t *testing.T) {
	for _, c := range []struct {
		states map[string]int
		total  int
		want   string
	}{
		{map[string]int{}, 0, templates.OverallNone},
		{map[string]int{"paused": 2}, 2, templates.OverallNone},
		{map[string]int{"up": 3}, 3, templates.OverallUp},
		{map[string]int{"down": 2, "paused": 1}, 3, templates.OverallMajor},
		{map[string]int{"down": 1, "up": 1}, 2, templates.OverallPartial},
		{map[string]int{"flapping": 1, "maintenance": 1}, 2, templates.OverallDegraded},
		{map[string]int{"pending": 1}, 1, templates.OverallDegraded},
		{map[string]int{"maintenance": 1, "up": 1}, 2, templates.OverallMaintenance},
	} {
		if got, _ := overall(c.states, c.total); got != c.want {
			t.Errorf("overall(%v) = %s, want %s", c.states, got, c.want)
		}
	}
}

func TestPublicCache(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	m := e.addMonitor(t, "x", "https://x.example.com/")
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	id := e.addPage(t, store.StatusPageInput{Slug: "c", Title: "C", Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Thing"}}})
	h := NewPublic(e.db, time.UTC, quietLoggerOnly())
	now := time.Now()
	h.now = func() time.Time { return now }
	read := func() string {
		p, err := store.GetStatusPage(ctx, e.db.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		v, err := h.view(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return v.Overall
	}
	if got := read(); got != templates.OverallUp {
		t.Fatalf("first = %s", got)
	}
	e.exec(t, `UPDATE monitors SET current_state = 'down'`)
	if got := read(); got != templates.OverallUp {
		t.Errorf("within the TTL = %s, want the cached up", got)
	}
	now = now.Add(publicCacheTTL)
	if got := read(); got != templates.OverallMajor {
		t.Errorf("after the TTL = %s, want major", got)
	}
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	e.exec(t, `UPDATE status_pages SET updated_at = ? WHERE id = ?`, store.FormatTime(now.Add(time.Second)), id)
	if got := read(); got != templates.OverallUp {
		t.Errorf("after a save = %s, want fresh figures", got)
	}
}

func TestAccentCSS(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"red":               "",
		"#3e67a8;}body{x:y": "",
		"#3E67A8 ":          "html[data-theme]{--accent:#3e67a8;--accent-contrast:#ffffff}",
		"#f0e060":           "html[data-theme]{--accent:#f0e060;--accent-contrast:#000000}",
	} {
		if got := accentCSS(in); got != want {
			t.Errorf("accentCSS(%q) = %q, want %q", in, got, want)
		}
	}
}
