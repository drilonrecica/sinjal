package browser

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	_ "modernc.org/sqlite"
)

// TestScreenshots is `make screenshots` (docs/20 "Screenshots"): it boots
// Sinjal on demo data and saves one PNG per page, theme, density and
// screen size into $SINJAL_SCREENSHOTS, with index.json listing them. It
// asserts nothing about the pictures; it fails only when a page breaks or
// writes to the browser console. Without the variable it skips, so
// `make test-browser` never runs it.
//
// The demo data is made the way a user would make it (setup, the monitor
// and status page forms, a published note), except the past: 90 days of
// history are written straight into SQLite while the server is stopped,
// and the engine reads them back on its next start.
func TestScreenshots(t *testing.T) {
	out := os.Getenv("SINJAL_SCREENSHOTS")
	if out == "" {
		t.Skip("set SINJAL_SCREENSHOTS to an output directory (make screenshots)")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dataDir := filepath.Join(t.TempDir(), "data")

	ctx := newBrowser(t)
	s := startIn(t, dataDir, addr)
	createAdmin(t, ctx, s)
	signIn(t, ctx, s, adminPassword)

	demo := newDemo(t)
	ids := demo.create(t, ctx, s)

	s.stop()
	seedHistory(t, filepath.Join(dataDir, "sinjal.db"), ids)
	demo.down.Store(true)
	s = startIn(t, dataDir, addr)
	// Watched from here: the restart itself cuts the open event stream.
	errs := watchConsole(t, ctx)

	demo.finish(t, ctx, s, ids)

	sh := &shooter{t: t, out: out}
	sh.matrix(ctx, s, ids["Search"])
	sh.statusPages(ctx, s, demo.page)
	sh.extras(ctx, s)
	sh.writeIndex()

	for _, e := range errs() {
		t.Errorf("browser console: %s", e)
	}
}

// --- Demo data ---

// demoMonitor is one monitor of the demo instance.
type demoMonitor struct {
	name, kind, tags string
	latency          time.Duration // typical answer time of its target
	interval         string
}

var demoMonitors = []demoMonitor{
	{"Checkout API", "http", "prod, api", 42 * time.Millisecond, "30"},
	{"Billing", "http", "prod, api", 85 * time.Millisecond, "30"},
	{"Auth service", "http", "prod", 24 * time.Millisecond, "30"},
	{"Search", "http", "prod, api", 140 * time.Millisecond, "10"}, // goes down
	{"Marketing site", "http", "web", 210 * time.Millisecond, "30"},
	{"Image CDN", "http", "web", 18 * time.Millisecond, "30"},
	{"Postgres primary", "tcp", "prod, db", 0, "30"},
	{"Nightly backup", "heartbeat", "jobs", 0, ""},
	{"Staging API", "http", "staging", 60 * time.Millisecond, "30"}, // paused
}

type demo struct {
	targets map[string]*httptest.Server
	down    atomic.Bool // the Search target answers 503 when set
	page    string      // the status page's edit path
}

// newDemo starts one local target per HTTP monitor, answering after about
// its latency, and one TCP listener for the database monitor.
func newDemo(t *testing.T) *demo {
	t.Helper()
	d := &demo{targets: map[string]*httptest.Server{}}
	for _, m := range demoMonitors {
		if m.kind == "heartbeat" {
			continue
		}
		latency, failing := m.latency, m.name == "Search"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Duration(float64(latency) * (0.8 + 0.4*rand.Float64())))
			if failing && d.down.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}))
		t.Cleanup(srv.Close)
		d.targets[m.name] = srv
	}
	return d
}

// create makes every demo monitor through the create form and returns
// their ids by name.
func (d *demo) create(t *testing.T, ctx context.Context, s *server) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, m := range demoMonitors {
		steps := []chromedp.Action[chromedp.Void]{
			chromedp.Navigate(s.base + "/monitors/new?type=" + m.kind),
			chromedp.SendKeys(chromedp.CSS("#name"), m.name),
			chromedp.SendKeys(chromedp.CSS("#tags"), m.tags),
		}
		switch m.kind {
		case "http":
			steps = append(steps, chromedp.SendKeys(chromedp.CSS("#url"), d.targets[m.name].URL+"/health"))
		case "tcp":
			host, port, _ := net.SplitHostPort(strings.TrimPrefix(d.targets["Checkout API"].URL, "http://"))
			steps = append(steps,
				chromedp.SendKeys(chromedp.CSS("#host"), host),
				chromedp.SetValue(chromedp.CSS("#port"), port))
		case "heartbeat":
			steps = append(steps,
				chromedp.SetValue(chromedp.CSS("#expected_interval"), "86400"),
				chromedp.SetValue(chromedp.CSS("#grace"), "3600"))
		}
		if m.interval != "" {
			steps = append(steps, chromedp.SetValue(chromedp.CSS("#interval"), m.interval))
		}
		if m.name == "Search" {
			steps = append(steps, chromedp.SetValue(chromedp.CSS("#failure_threshold"), "1"))
		}
		steps = append(steps, chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)))
		if m.kind == "heartbeat" {
			// A heartbeat monitor ends on its one-time push URL.
			steps = append(steps, chromedp.WaitVisible(chromedp.CSS("#push-url")))
			do(t, ctx, "create "+m.name, steps...)
			push := text(t, ctx, "#push-url")
			ids[m.name] = push[strings.LastIndex(push, "/")+1:] // the token, until finish resolves the id
			continue
		}
		steps = append(steps, chromedp.WaitVisible(chromedp.CSS("header.monitor-header")))
		do(t, ctx, "create "+m.name, steps...)
		ids[m.name] = get(t, ctx, "read the id of "+m.name,
			chromedp.AttributeValue(chromedp.CSS("header.monitor-header"), "data-monitor-id")).Value
	}
	return ids
}

// finish runs after the restart: a beat for the heartbeat, the paused
// monitor, Search down with a published note, and the status page.
func (d *demo) finish(t *testing.T, ctx context.Context, s *server, ids map[string]string) {
	t.Helper()
	token := ids["Nightly backup"]
	resp, err := http.Post(s.base+"/api/v1/heartbeat/"+token, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	delete(ids, "Nightly backup")

	do(t, ctx, "pause Staging API",
		chromedp.Navigate(s.base+"/monitors/"+ids["Staging API"]),
		chromedp.Click(chromedp.CSS(`form[action$="/pause"] button`)),
		chromedp.WaitVisible(chromedp.CSS(`header.monitor-header[data-state="paused"]`)),
	)

	do(t, ctx, "open Search", chromedp.Navigate(s.base+"/monitors/"+ids["Search"]))
	waitState(t, ctx, "header.monitor-header", "down", 40*time.Second)

	do(t, ctx, "publish a note on the Search incident",
		chromedp.Navigate(s.base+"/incidents"),
		chromedp.Click(chromedp.CSS(`a[href^="/incidents/"]`)),
		chromedp.WaitVisible(chromedp.CSS("#note")),
		chromedp.SendKeys(chromedp.CSS("#note"), "Search is returning errors for some queries. We are rolling back the last deploy."),
		chromedp.Click(chromedp.CSS(`input[name="publish"]`)),
		chromedp.Click(chromedp.CSS(`form.incident-note button[type="submit"]`)),
		chromedp.Poll[chromedp.Void](`document.body.innerText.includes("rolling back")`, chromedp.WithPollingTimeout(10*time.Second)),
	)

	steps := []chromedp.Action[chromedp.Void]{
		chromedp.Navigate(s.base + "/status-pages/new"),
		chromedp.SendKeys(chromedp.CSS("#title"), "Acme status"),
		chromedp.SendKeys(chromedp.CSS("#slug"), "acme"),
		chromedp.SendKeys(chromedp.CSS("#description"), "Live status of Acme's public services."),
		chromedp.Click(chromedp.CSS("#visibility-public")),
	}
	for name, public := range map[string]string{
		"Checkout API": "Checkout", "Billing": "Billing", "Auth service": "Sign-in",
		"Search": "Search", "Marketing site": "Website", "Image CDN": "Images",
	} {
		steps = append(steps,
			chromedp.Click(chromedp.CSS("#m_show_"+ids[name])),
			chromedp.SendKeys(chromedp.CSS("#m_name_"+ids[name]), public))
	}
	steps = append(steps,
		chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS(".sp-list")))
	do(t, ctx, "create the status page", steps...)
	d.page = get(t, ctx, "read the edit link", chromedp.AttributeValue(chromedp.CSS(`.sp-list a[href$="/edit"]`), "href")).Value
}

// seedHistory writes the past into the stopped server's database: every
// monitor created 90 days ago, 7 days of check results (every 5 minutes,
// then at the real interval for the last two hours) and a few short
// outages, each an ended incident with its events and failed results;
// the heartbeat monitor gets a week of nightly beats.
func seedHistory(t *testing.T, path string, ids map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ts := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	now := time.Now().Truncate(time.Second)

	type outage struct {
		monitor string
		ago     time.Duration // how long ago it started
		lasted  time.Duration
	}
	// Oldest first: incident and event ids grow with time, as in real use.
	outages := []outage{
		{"Billing", 61 * 24 * time.Hour, 40 * time.Minute},
		{"Image CDN", 45 * 24 * time.Hour, 90 * time.Minute},
		{"Billing", 23 * 24 * time.Hour, 12 * time.Minute},
		{"Marketing site", 4*24*time.Hour + 3*time.Hour, 25 * time.Minute},
		{"Checkout API", 2*24*time.Hour + 9*time.Hour, 6 * time.Minute},
	}
	failingAt := func(name string, at time.Time) bool {
		for _, o := range outages {
			start := now.Add(-o.ago)
			if o.monitor == name && !at.Before(start) && at.Before(start.Add(o.lasted)) {
				return true
			}
		}
		return false
	}

	exec(`UPDATE monitors SET created_at = ?`, ts(now.Add(-90*24*time.Hour)))
	for _, m := range demoMonitors {
		id, ok := ids[m.name]
		if !ok || m.kind == "heartbeat" {
			continue
		}
		base := float64(m.latency.Milliseconds())
		if m.kind == "tcp" {
			base = 1.5
		}
		step := 5 * time.Minute
		for at := now.Add(-7 * 24 * time.Hour); at.Before(now.Add(-time.Minute)); at = at.Add(step) {
			if now.Sub(at) < 2*time.Hour {
				step = 30 * time.Second
			}
			ms := base * (0.75 + 0.5*rand.Float64())
			if rand.IntN(60) == 0 {
				ms *= 2.5 // the odd slow answer
			}
			if failingAt(m.name, at) {
				exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success, protocol_status, error_kind, error_message)
					VALUES (?, ?, ?, 0, '503', 'status', 'status 503, expected 200-399')`, id, ts(at), ms)
				continue
			}
			status := any("200")
			if m.kind == "tcp" {
				status = nil
			}
			exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success, protocol_status)
				VALUES (?, ?, ?, 1, ?)`, id, ts(at), ms, status)
		}
	}
	// The nightly job beat at 02:00 UTC on each of the last seven days, so
	// it is not overdue when the server starts again.
	var beatID string
	if err := tx.QueryRow(`SELECT monitor_id FROM heartbeat_monitor_config`).Scan(&beatID); err != nil {
		t.Fatal(err)
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, time.UTC)
	if day.After(now) {
		day = day.AddDate(0, 0, -1)
	}
	for i := 6; i >= 0; i-- {
		exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, 0, 1)`, beatID, ts(day.AddDate(0, 0, -i)))
	}
	exec(`UPDATE heartbeat_monitor_config SET last_beat_at = ?`, ts(day))

	for i, o := range outages {
		start := now.Add(-o.ago)
		id := fmt.Sprintf("demo%028d", i)
		exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, initial_failure_kind, summary, created_at)
			VALUES (?, ?, ?, ?, 'status', 'status 503, expected 200-399', ?)`,
			id, ids[o.monitor], ts(start), ts(start.Add(o.lasted)), ts(start.Add(30*time.Second)))
		for _, e := range []struct {
			typ string
			at  time.Time
		}{{"detected", start}, {"declared_down", start.Add(30 * time.Second)}, {"recovered", start.Add(o.lasted)}} {
			exec(`INSERT INTO incident_events (incident_id, event_type, created_at) VALUES (?, ?, ?)`, id, e.typ, ts(e.at))
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// --- Shots ---

type size struct {
	name          string
	width, height int64
	scale         float64
	phone         bool
}

var (
	desktop   = size{"desktop", 1440, 900, 1, false}
	ultrawide = size{"ultrawide", 2560, 1080, 1, false}
	phone     = size{"phone", 390, 844, 2, true}
)

// variant is one appearance: a theme, with Stark's Ambient light on or off.
type variant struct {
	name, theme string
	ambient     bool
}

var variants = []variant{
	{"stark", "stark", true},
	{"stark-noambient", "stark", false},
	{"terminal", "terminal", false},
	{"daylight", "daylight", false},
}

type shot struct {
	File    string `json:"file"`
	Page    string `json:"page"`
	Theme   string `json:"theme"`
	Density string `json:"density,omitempty"`
	Size    string `json:"size"`
	Width   int64  `json:"width"`
	Height  int64  `json:"height"`
}

type shooter struct {
	t     *testing.T
	out   string
	shots []shot
}

// matrix shoots Home and the down monitor's detail (and, on phones, the
// list view) for every variant, density and size.
func (sh *shooter) matrix(ctx context.Context, s *server, down string) {
	t := sh.t
	for _, v := range variants {
		for _, density := range []string{"comfortable", "compact"} {
			setAppearance(t, ctx, s, v, density)
			for _, sz := range []size{desktop, ultrawide, phone} {
				pages := []struct{ name, path string }{{"home", "/"}, {"monitor", "/monitors/" + down}}
				if sz.phone {
					pages = append(pages, struct{ name, path string }{"list", "/monitors"})
				}
				for _, p := range pages {
					sh.take(ctx, s.base+p.path, shot{Page: p.name, Theme: v.name, Density: density, Size: sz.name}, sz)
				}
			}
		}
	}
}

// statusPages shoots the public page in each of its themes, as a stranger.
func (sh *shooter) statusPages(ctx context.Context, s *server, edit string) {
	t := sh.t
	v := visitor(t, ctx)
	for _, theme := range []string{"daylight", "stark", "terminal"} {
		do(t, ctx, "set the status page theme to "+theme,
			chromedp.Navigate(s.base+edit),
			chromedp.SetValue(chromedp.CSS("#theme"), theme),
			chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
			chromedp.WaitVisible(chromedp.CSS(".sp-list")),
		)
		for _, sz := range []size{desktop, phone} {
			sh.take(v, s.base+"/status/acme", shot{Page: "status-page", Theme: theme, Size: sz.name}, sz)
		}
	}
}

// extras: the command palette, the phone menu open and the sign-in page,
// in Stark.
func (sh *shooter) extras(ctx context.Context, s *server) {
	t := sh.t
	setAppearance(t, ctx, s, variants[0], "comfortable")
	sh.take(ctx, s.base+"/", shot{Page: "palette", Theme: "stark", Size: desktop.name}, desktop,
		chromedp.KeyEvent("k", chromedp.KeyModifiers(chromedp.ModifierCtrl)),
		chromedp.WaitVisible(chromedp.CSS("dialog.palette[open]")),
		chromedp.SendKeys(chromedp.CSS("dialog.palette input"), "se"),
		chromedp.Sleep(500*time.Millisecond),
	)
	sh.take(ctx, s.base+"/", shot{Page: "phone-menu", Theme: "stark", Size: phone.name}, phone,
		chromedp.Click(chromedp.CSS(".topbar-menu-button")),
		chromedp.WaitVisible(chromedp.CSS("#topnav")),
	)
	v := visitor(t, ctx)
	sh.take(v, s.base+"/login", shot{Page: "sign-in", Theme: "stark", Size: desktop.name}, desktop)
}

// take sizes the tab, opens url, waits for the page to settle, runs then,
// and saves a screenshot of the viewport.
func (sh *shooter) take(ctx context.Context, url string, sc shot, sz size, then ...chromedp.Action[chromedp.Void]) {
	t := sh.t
	t.Helper()
	opts := []chromedp.EmulateViewportOption{chromedp.EmulateScale(sz.scale)}
	if sz.phone {
		opts = append(opts, chromedp.EmulateMobile, chromedp.EmulateTouch)
	}
	steps := []chromedp.Action[chromedp.Void]{
		chromedp.EmulateViewport(sz.width, sz.height, opts...),
		reducedMotion(),
		chromedp.Navigate(url),
		chromedp.WaitReady(chromedp.CSS("body")),
		// The column fills itself after load; the pictures need its rows.
		chromedp.Poll[chromedp.Void](`!document.querySelector(".column-body") || document.querySelectorAll(".column-row").length > 0 || getComputedStyle(document.querySelector(".monitor-column")).display === "none"`,
			chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Poll[chromedp.Void](`document.fonts.status === "loaded"`, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Sleep(700 * time.Millisecond),
	}
	steps = append(steps, then...)
	do(t, ctx, "open "+url, steps...)
	png := get(t, ctx, "screenshot "+url, chromedp.CaptureScreenshot())

	parts := []string{sc.Page, sc.Theme}
	if sc.Density != "" {
		parts = append(parts, sc.Density)
	}
	parts = append(parts, sc.Size)
	sc.File = strings.Join(parts, "-") + ".png"
	sc.Width, sc.Height = sz.width, sz.height
	if err := os.WriteFile(filepath.Join(sh.out, sc.File), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sh.shots = append(sh.shots, sc)
}

func (sh *shooter) writeIndex() {
	b, err := json.MarshalIndent(sh.shots, "", "  ")
	if err != nil {
		sh.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sh.out, "index.json"), append(b, '\n'), 0o644); err != nil {
		sh.t.Fatal(err)
	}
	sh.t.Logf("%d screenshots in %s", len(sh.shots), sh.out)
}

// setAppearance saves theme, density and Ambient light through the
// Appearance form, as a user would.
func setAppearance(t *testing.T, ctx context.Context, s *server, v variant, density string) {
	t.Helper()
	do(t, ctx, "open Appearance",
		chromedp.EmulateViewport(desktop.width, desktop.height),
		chromedp.Navigate(s.base+"/account/appearance"),
		chromedp.Click(chromedp.CSS("#theme-"+v.theme)),
		chromedp.Click(chromedp.CSS("#density-"+density)),
	)
	if eval[bool](t, ctx, `document.querySelector("#ambient").checked`) != v.ambient {
		do(t, ctx, "toggle Ambient light", chromedp.Click(chromedp.CSS("#ambient")))
	}
	do(t, ctx, "save the appearance",
		chromedp.Click(chromedp.CSS(`form[action="/account/appearance"] button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS(".auth-notice")),
	)
}

// reducedMotion asks for prefers-reduced-motion, so no picture catches a
// transition halfway.
func reducedMotion() chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}},
		})
		return err
	})
}

// watchConsole collects console errors and uncaught exceptions of the tab
// for the whole test; the returned func reads them.
func watchConsole(t *testing.T, ctx context.Context) func() []string {
	t.Helper()
	var mu sync.Mutex
	var found []string
	messages := chromedp.Console(ctx)
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleError || m.IsException() {
				mu.Lock()
				found = append(found, m.String()+" ("+m.URL+")")
				mu.Unlock()
			}
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), found...)
	}
}
