package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

var testMonitor = MonitorView{
	ID: "m1", Name: "API", Type: "http", State: StateUp, Since: "3m", SinceAt: "2026-10-06T12:00:00Z",
	Target: "https://api.example.com", Latency: "128 ms", LastCheck: "20s ago", Uptime: "—",
	Parent: "Gateway", Tags: []string{"prod", "eu"},
}

func renderToString(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A column row: one link with the state (the word for assistive
// technology), name, uptime (saying what the figure is) and beat bar, read
// in that order; it refreshes itself.
func TestColumnRow(t *testing.T) {
	m := testMonitor
	m.Uptime = "99.50%"
	html := renderToString(t, ColumnRow(m))
	for _, want := range []string{
		`id="col-m1"`, `data-live`, `data-monitor-id="m1"`, `data-state="up"`,
		`hx-get="/fragments/monitors/m1/row"`, `hx-trigger="refresh"`, `hx-swap="outerHTML"`,
		`<a class="column-link" href="/monitors/m1">`, `<span class="visually-hidden">Up:</span>`,
		`<span class="column-name">API</span>`, `>99.50% <span class="visually-hidden">uptime, last 24 hours</span></span>`, `class="beat-bar"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("column row lacks %q:\n%s", want, html)
		}
	}
}

func TestColumnList(t *testing.T) {
	html := renderToString(t, ColumnList([]MonitorView{testMonitor}, false, "", false))
	if !strings.Contains(html, `data-live-list hx-get="/fragments/monitors"`) || !strings.Contains(html, `id="col-m1"`) || strings.Contains(html, "No monitors yet") {
		t.Fatalf("column list:\n%s", html)
	}
	if html := renderToString(t, ColumnList(nil, true, "", false)); !strings.Contains(html, "No monitors yet.") || !strings.Contains(html, `href="/monitors/new"`) {
		t.Fatalf("empty column for an admin:\n%s", html)
	}
	if html := renderToString(t, ColumnList(nil, false, "", false)); strings.Contains(html, `/monitors/new`) {
		t.Fatalf("empty column offers a viewer to create:\n%s", html)
	}
}

// Status is never colour alone: every state has visible text and a glyph.
func TestStatusBadgeHasTextForEveryState(t *testing.T) {
	want := map[string]string{StateUp: "Up", StateDown: "Down", StatePending: "Pending", StatePaused: "Paused", StateFlapping: "Flapping", "": "Pending"}
	glyphs := map[string]bool{}
	for state, label := range want {
		html := renderToString(t, StatusBadge(state))
		if !strings.Contains(html, `<span class="status-text">`+label+`</span>`) {
			t.Errorf("%q: no visible text %q in %s", state, label, html)
		}
		if !strings.Contains(html, `aria-hidden="true"`) {
			t.Errorf("%q: icon is not hidden from screen readers", state)
		}
		glyphs[stateIcon(state)] = true
	}
	if len(glyphs) != 5 { // pending shares its icon with the unknown state
		t.Errorf("states do not each have their own glyph: %v", glyphs)
	}
}

func TestMonitorNameIsEscaped(t *testing.T) {
	m := testMonitor
	m.Name = `<img src=x onerror=alert(1)>`
	m.Tags = []string{`"><script>`}
	for name, c := range map[string]templ.Component{"row": ColumnRow(m), "header": MonitorHeader(m)} {
		html := renderToString(t, c)
		if strings.Contains(html, "<img") || strings.Contains(html, "<script") {
			t.Errorf("%s does not escape names and tags:\n%s", name, html)
		}
	}
}

func TestMonitorHeaderRefreshesFromItsFragment(t *testing.T) {
	html := renderToString(t, MonitorHeader(testMonitor))
	for _, want := range []string{
		`id="monitor-header-m1"`, `data-monitor-id="m1"`, `hx-get="/fragments/monitors/m1/header"`,
		`hx-trigger="refresh"`, `<h1>API</h1>`, `20s ago`, `128 ms`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("header lacks %q", want)
		}
	}
}

func TestLiveWrapsChildrenInOneEventSource(t *testing.T) {
	var buf bytes.Buffer
	if err := Live().Render(templ.WithChildren(context.Background(), templ.Raw("<p>x</p>")), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{`hx-ext="sse"`, `sse-connect="/events"`,
		`hx-trigger="sse:monitor.updated, sse:monitor.created, sse:monitor.deleted, sse:incident.opened, sse:incident.updated, sse:incident.closed, sse:maintenance.updated, sse:notification.channel_updated"`, "<p>x</p>"} {
		if !strings.Contains(html, want) {
			t.Errorf("live region lacks %q: %s", want, html)
		}
	}
	if strings.Count(html, "sse-connect") != 1 {
		t.Error("more than one event source")
	}
}

// A down parent is said in words on the detail header.
func TestParentDownIsShown(t *testing.T) {
	m := testMonitor
	for name, c := range map[string]func(MonitorView) templ.Component{
		"header": func(v MonitorView) templ.Component { return MonitorHeader(v) },
	} {
		if html := renderToString(t, c(m)); strings.Contains(html, "Parent down") {
			t.Errorf("%s: parent down shown while it is up", name)
		}
		m.ParentDown = true
		if html := renderToString(t, c(m)); !strings.Contains(html, "Parent down: notifications held") {
			t.Errorf("%s: parent down not shown:\n%s", name, html)
		}
		m.ParentDown = false
	}
}

// Terminal shows short state tags (docs/05 "[FLAP]"). Where the tag is not
// the word itself it is a separate aria-hidden span, so the word stays
// what assistive technology reads.
func TestStatusBadgeTerminalTag(t *testing.T) {
	html := renderToString(t, StatusBadge(StateFlapping))
	if !strings.Contains(html, `<span class="status-text">Flapping</span> <span class="status-tag" aria-hidden="true">FLAP</span>`) {
		t.Fatalf("flapping badge: %s", html)
	}
	for _, state := range []string{StateUp, StateDown, StatePaused, StatePending} {
		if html := renderToString(t, StatusBadge(state)); strings.Contains(html, "status-tag") {
			t.Errorf("%s badge has a tag of its own: %s", state, html)
		}
	}
}

// The problem strip is as loud as its worst line: red only for an outage
// or results not being saved, the warning colour otherwise.
func TestProblemStripSeverity(t *testing.T) {
	for _, tt := range []struct {
		states []string
		want   string
	}{
		{[]string{"channel", "tls"}, "warning"},
		{[]string{StateFlapping}, "warning"},
		{[]string{"tls", StateDown}, "down"},
		{[]string{"system"}, "down"},
	} {
		var ps []Problem
		for _, s := range tt.states {
			ps = append(ps, Problem{State: s, Text: s})
		}
		html := renderToString(t, OverviewBody(OverviewView{Monitors: 1, Problems: ps}))
		if !strings.Contains(html, `<section class="problem-strip" data-severity="`+tt.want+`"`) {
			t.Errorf("%v: not %s:\n%s", tt.states, tt.want, html)
		}
	}
}
