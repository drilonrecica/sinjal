package browser

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestStatusPageLayouts opens one public status page in each layout and
// theme as a stranger, at 1440 and 390 px (docs/20 "UI/browser tests"):
// the page never scrolls sideways and writes nothing to the console, no
// mark moves under reduced motion, and in Logbook a day's link opens that
// day's panel in place of the newest one.
func TestStatusPageLayouts(t *testing.T) {
	ctx := newBrowser(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dataDir := filepath.Join(t.TempDir(), "data")
	s := startIn(t, dataDir, addr)
	createAdmin(t, ctx, s)
	signIn(t, ctx, s, adminPassword)
	tg := newTarget(t)
	mon := createHTTPMonitor(t, ctx, s, "checkout-api (internal)", tg.URL+"/health", "30")
	do(t, ctx, "create the status page",
		chromedp.Navigate(s.base+"/status-pages/new"),
		chromedp.SendKeys(chromedp.CSS("#title"), "Acme status"),
		chromedp.SendKeys(chromedp.CSS("#slug"), "acme"),
		chromedp.Click(chromedp.CSS("#visibility-public")),
		chromedp.Click(chromedp.CSS("#m_show_"+mon)),
		chromedp.SendKeys(chromedp.CSS("#m_name_"+mon), "Checkout"),
		chromedp.Click(chromedp.CSS("#m_latency_"+mon)),
		chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS(".sp-list")),
	)
	edit := get(t, ctx, "read the edit link", chromedp.AttributeValue(chromedp.CSS(`.sp-list a[href$="/edit"]`), "href")).Value

	// Two ended incidents, three and five days ago, so the calendar has an
	// incident day besides the newest one.
	s.stop()
	pastIncidents(t, filepath.Join(dataDir, "sinjal.db"), mon)
	s = startIn(t, dataDir, addr)

	v := visitor(t, ctx)
	errs := watchConsole(t, v)
	still := visitor(t, ctx)
	do(t, still, "ask for reduced motion", reducedMotion())
	for _, layout := range []string{"signal", "sentence", "heartline", "logbook"} {
		for _, theme := range []string{"daylight", "stark", "terminal"} {
			do(t, ctx, "set the page to "+layout+" in "+theme,
				chromedp.Navigate(s.base+edit),
				chromedp.Click(chromedp.CSS(`label[for="layout-`+layout+`"]`)),
				chromedp.SetValue(chromedp.CSS("#theme"), theme),
				chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
				chromedp.WaitVisible(chromedp.CSS(".sp-list")),
			)
			for _, sz := range []size{desktop, phone} {
				name := fmt.Sprintf("%s/%s at %d px", layout, theme, sz.width)
				opts := []chromedp.EmulateViewportOption{chromedp.EmulateScale(sz.scale)}
				if sz.phone {
					opts = append(opts, chromedp.EmulateMobile, chromedp.EmulateTouch)
				}
				do(t, v, "size the tab for "+name, chromedp.EmulateViewport(sz.width, sz.height, opts...))
				openLayout(t, v, s.base+"/status/acme", layout)
				if got := eval[string](t, v, `document.documentElement.dataset.theme`); got != theme {
					t.Errorf("%s: theme %q", name, got)
				}
				if w := eval[string](t, v, `document.scrollingElement.scrollWidth + "/" + innerWidth`); !eval[bool](t, v, `document.scrollingElement.scrollWidth <= innerWidth`) {
					t.Errorf("%s scrolls sideways: scroll width/viewport %s", name, w)
				}
				if layout == "logbook" {
					openOlderDay(t, v, name)
				}
			}
			openLayout(t, still, s.base+"/status/acme", layout)
			if moving := eval[string](t, still, movingJS); moving != "" {
				t.Errorf("%s/%s moves under reduced motion: %s", layout, theme, moving)
			}
		}
	}
	for _, e := range errs() {
		t.Errorf("browser console: %s", e)
	}
}

// openLayout opens url until it shows layout. A save in the same second
// as the cached view keeps the old view for up to its 10 s
// (publicCacheTTL; updated_at counts whole seconds).
func openLayout(t *testing.T, ctx context.Context, url, layout string) {
	t.Helper()
	for deadline := time.Now().Add(12 * time.Second); ; {
		do(t, ctx, "open "+url, chromedp.Navigate(url), chromedp.WaitVisible(chromedp.CSS(".public-page")))
		if eval[string](t, ctx, `document.querySelector(".public-page").dataset.layout`) == layout {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s does not show %s", url, layout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// movingJS names the elements (and pseudo-elements) whose animation still
// loops or lasts under reduced motion; "" when none does.
const movingJS = `[...document.querySelectorAll("*")].flatMap(e => [null, "::before", "::after"].map(p => [e, p, getComputedStyle(e, p)]))
	.filter(([, , s]) => s.animationName !== "none" && (s.animationIterationCount === "infinite" || parseFloat(s.animationDuration) > 0.01))
	.map(([e, p]) => e.className + (p || "")).join(", ")`

// openOlderDay clicks the calendar's incident day that is not the newest:
// its panel opens and the newest day's panel, open by default, closes.
func openOlderDay(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	day := get(t, ctx, name+": find the older incident day", chromedp.AttributeValue(chromedp.CSS(`a.cal-day:not([data-newest])`), "id")).Value
	if day == "" {
		t.Fatalf("%s: no older incident day in the calendar", name)
	}
	do(t, ctx, name+": open "+day, chromedp.Click(chromedp.CSS("#"+day)))
	shown := func(sel string) bool {
		return eval[bool](t, ctx, `getComputedStyle(document.querySelector(`+fmt.Sprintf("%q", sel)+`)).display !== "none"`)
	}
	if got := eval[string](t, ctx, `location.hash`); got != "#"+day {
		t.Errorf("%s: the link went to %q", name, got)
	}
	if !shown("#" + day + " + .cal-panel") {
		t.Errorf("%s: %s's panel did not open", name, day)
	}
	if shown(".cal-panel[data-newest]") {
		t.Errorf("%s: the newest day's panel stayed open", name)
	}
}

// pastIncidents writes two ended incidents of monitor into the stopped
// server's database, with their events.
func pastIncidents(t *testing.T, path, monitor string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ts := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	now := time.Now().Truncate(time.Second)
	for i, ago := range []int{5, 3} {
		start := now.AddDate(0, 0, -ago)
		id := fmt.Sprintf("layout%026d", i)
		if _, err := db.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, initial_failure_kind, summary, created_at)
			VALUES (?, ?, ?, ?, 'status', 'status 503, expected 200-399', ?)`, id, monitor, ts(start), ts(start.Add(12*time.Minute)), ts(start)); err != nil {
			t.Fatal(err)
		}
		for _, e := range []struct {
			typ string
			at  time.Time
		}{{"detected", start}, {"declared_down", start}, {"recovered", start.Add(12 * time.Minute)}} {
			if _, err := db.Exec(`INSERT INTO incident_events (incident_id, event_type, created_at) VALUES (?, ?, ?)`, id, e.typ, ts(e.at)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
