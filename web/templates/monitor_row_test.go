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

func TestMonitorRowShowsEveryField(t *testing.T) {
	html := renderToString(t, MonitorRow(testMonitor))
	for _, want := range []string{
		`id="monitor-m1"`, `data-live`, `data-monitor-id="m1"`,
		`hx-get="/fragments/monitors/m1/row"`, `hx-trigger="refresh"`, `hx-swap="outerHTML"`,
		`href="/monitors/m1"`, `>API</a>`, `>http</span>`, `https://api.example.com`,
		`128 ms`, `datetime="2026-10-06T12:00:00Z"`, `>3m</time>`,
		`Depends on Gateway`, `>prod</li>`, `>eu</li>`, `>Up</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("row lacks %q:\n%s", want, html)
		}
	}
}

func TestMonitorRowOmitsWhatIsNotThere(t *testing.T) {
	m := testMonitor
	m.Target, m.Parent, m.Tags = "", "", nil
	html := renderToString(t, MonitorRow(m))
	for _, bad := range []string{"monitor-target", "monitor-dependency", "tag-list"} {
		if strings.Contains(html, bad) {
			t.Errorf("row has %q without data", bad)
		}
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
			t.Errorf("%q: glyph is not hidden from screen readers", state)
		}
		glyphs[stateGlyph(state)] = true
	}
	if len(glyphs) != 5 { // pending shares its glyph with the unknown state
		t.Errorf("states do not each have their own glyph: %v", glyphs)
	}
}

func TestMonitorNameIsEscaped(t *testing.T) {
	m := testMonitor
	m.Name = `<img src=x onerror=alert(1)>`
	m.Tags = []string{`"><script>`}
	for name, c := range map[string]templ.Component{"row": MonitorRow(m), "header": MonitorHeader(m)} {
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
	for _, want := range []string{`hx-ext="sse"`, `sse-connect="/events"`, `hx-trigger="sse:monitor.updated"`, "<p>x</p>"} {
		if !strings.Contains(html, want) {
			t.Errorf("live region lacks %q: %s", want, html)
		}
	}
	if strings.Count(html, "sse-connect") != 1 {
		t.Error("more than one event source")
	}
}
