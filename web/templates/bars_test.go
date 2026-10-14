package templates

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
)

var testBeats = BeatBarView{
	Beats:  []Beat{{Result: BeatUp, Tip: "12:00:30 · 128 ms · Up"}, {Result: BeatDown, Tip: "12:00:00 · 5.0 s · Down: Timeout"}, {}},
	Newest: 1760443230, Label: "Last 2 checks: 1 up, 1 down. Latest: up at 12:00:30.",
}

func TestBeatBar(t *testing.T) {
	html := renderToString(t, BeatBar(testBeats))
	want := `<div class="beat-bar" data-beat-bar data-newest="1760443230" role="img" aria-label="Last 2 checks: 1 up, 1 down. Latest: up at 12:00:30.">` +
		`<i data-b="up" data-tip="12:00:30 · 128 ms · Up"></i>` +
		`<i data-b="down" data-tip="12:00:00 · 5.0 s · Down: Timeout"></i>` +
		`<i></i></div>`
	if html != want {
		t.Fatalf("got\n%s\nwant\n%s", html, want)
	}
}

func TestRowBeatBarSaysWhereItsTipsAre(t *testing.T) {
	b := BeatBarView{Beats: []Beat{{Result: BeatUp}, {}}, Newest: 5, Label: "l", TipsHref: "/fragments/monitors/m1/beats?until=5"}
	want := `<div class="beat-bar" data-beat-bar data-newest="5" role="img" aria-label="l" data-tips="/fragments/monitors/m1/beats?until=5"><i data-b="up"></i><i></i></div>`
	if html := renderToString(t, BeatBar(b)); html != want {
		t.Fatalf("got\n%s\nwant\n%s", html, want)
	}
}

// The detail bar is a tab stop for keyboard users; a list row's is not,
// or a list of 1,000 monitors would have 1,000 more.
func TestBeatBarFocus(t *testing.T) {
	if html := renderToString(t, BeatBar(testBeats)); strings.Contains(html, "tabindex") {
		t.Fatal("a list bar is focusable")
	}
	b := testBeats
	b.Focusable = true
	if html := renderToString(t, BeatBar(b)); !strings.Contains(html, `tabindex="0"`) {
		t.Fatal("the detail bar is not focusable")
	}
}

func TestRowAndHeaderShowTheBeatBar(t *testing.T) {
	m := testMonitor
	m.Beats = testBeats
	for name, c := range map[string]templ.Component{"row": MonitorRow(m), "header": MonitorHeader(m)} {
		if html := renderToString(t, c); !strings.Contains(html, `data-tip="12:00:30 · 128 ms · Up"`) {
			t.Errorf("%s has no beat bar:\n%s", name, html)
		}
	}
}

func TestDayBars(t *testing.T) {
	html := renderToString(t, DayBars([]Day{{Class: DayUp, Label: "Mon 12 Oct 2026: 100.00% uptime, no incidents"}, {Class: DayNoData, Label: "Tue 13 Oct 2026: no data"}}, "Last 2 days"))
	want := `<ol class="day-bars" role="img" aria-label="Last 2 days">` +
		`<li data-day="up" title="Mon 12 Oct 2026: 100.00% uptime, no incidents"></li>` +
		`<li data-day="none" title="Tue 13 Oct 2026: no data"></li></ol>`
	if html != want {
		t.Fatalf("got\n%s\nwant\n%s", html, want)
	}
}
