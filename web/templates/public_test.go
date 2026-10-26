package templates

import (
	"regexp"
	"strings"
	"testing"
)

// publicSheet finds the public stylesheets by name, without their hash.
var publicSheet = regexp.MustCompile(`css/public[_a-z]*`)

// PublicStatusPage dispatches on the layout to its component and links
// that layout's stylesheet beside the shared one. A layout not built yet,
// an unknown and an empty one render as Sentence (docs/12).
func TestPublicStatusPageDispatch(t *testing.T) {
	for _, layout := range []string{LayoutSignal, LayoutSentence, LayoutHeartline, LayoutLogbook, "", "carousel"} {
		want := LayoutSentence
		if builtLayouts[layout] {
			want = layout
		}
		html := renderToString(t, PublicStatusPage(PublicPage{Layout: layout, Title: "P", Overall: OverallUp, Uptime: "—", IncidentDays: 30}, "daylight"))
		if !strings.Contains(html, `<div class="public-page" data-layout="`+want+`">`) {
			t.Errorf("layout %q does not render %s", layout, want)
		}
		if got := strings.Join(publicSheet.FindAllString(html, -1), " "); got != "css/public css/public_"+want {
			t.Errorf("layout %q links %s", layout, got)
		}
	}
	// The password form and message pages link the shared sheet alone.
	for _, html := range []string{renderToString(t, PublicPasswordPage(PublicPasswordView{Title: "P", Action: "/status/p"}, "daylight")),
		renderToString(t, PublicMessagePage("P", "m", "daylight"))} {
		if got := strings.Join(publicSheet.FindAllString(html, -1), " "); got != "css/public" {
			t.Errorf("a locked page links %s", got)
		}
	}
}

func TestPublicSharedPartials(t *testing.T) {
	v := PublicPage{Layout: LayoutSentence, Title: "Acme", Description: "What we run", Logo: "/uploads/l.png", FeedURL: "/s/tok/feed.xml",
		Overall: OverallUp, Uptime: "100.00%", IncidentDays: 30, Zone: "CET", PoweredBy: true,
		LastIssue: "No interruptions in the last 30 days.",
		Groups:    []PublicGroup{{Rows: []PublicRow{{Name: "Web", State: StateUp, Days: make([]Day, 90), Downtime: "No downtime in 90 days"}}}}}
	html := renderToString(t, PublicStatusPage(v, "daylight"))
	for _, want := range []string{
		`<img class="public-logo" src="/uploads/l.png" alt="">`,
		`<h1>Acme</h1><p class="public-description">What we run</p>`,
		`<a class="public-subscribe" href="/s/tok/feed.xml"><svg class="icon icon-rss"`,
		`<span>Subscribe</span>`,
		"Times in CET. The page refreshes every minute.",
		`Powered by <a href="https://github.com/drilonrecica/sinjal">Sinjal</a>`,
		"No interruptions in the last 30 days.",
		"No downtime in 90 days",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// One axis per page, not one per service.
	v.Groups = append(v.Groups, v.Groups[0])
	if n := strings.Count(renderToString(t, PublicStatusPage(v, "daylight")), `class="public-axis"`); n != 1 {
		t.Errorf("%d axes", n)
	}
	v.PoweredBy, v.FeedURL = false, ""
	if html := renderToString(t, PublicStatusPage(v, "daylight")); strings.Contains(html, "Powered by") || strings.Contains(html, "public-subscribe") {
		t.Error("Powered by or Subscribe without them")
	}
}

// The "Monitoring since" form: the days before the first day with data
// are dashed cells without words of their own; the label shows when they
// leave room for it.
func TestDayBarsSince(t *testing.T) {
	days := make([]Day, 90)
	for i := range days {
		days[i] = Day{Class: DayNoData, Label: "no data"}
	}
	days[89] = Day{Class: DayUp, Label: "Mon 26 Oct 2026: 100.00% uptime, no incidents"}
	html := renderToString(t, PublicDayBars(PublicRow{Days: days, Since: "Monitoring since 26 Oct", SinceDay: 89, StripText: "s"}))
	if !strings.HasPrefix(html, `<ol class="day-bars" role="img" aria-label="s"><li class="day-bars-since">Monitoring since 26 Oct</li><li data-day="before"></li>`) ||
		strings.Count(html, `data-day="before"`) != 89 || strings.Contains(html, "no data") ||
		!strings.HasSuffix(html, `<li data-day="up" title="Mon 26 Oct 2026: 100.00% uptime, no incidents"></li></ol>`) {
		t.Errorf("since 89:\n%s", html)
	}
	days[88], days[87] = days[89], days[89]
	html = renderToString(t, PublicDayBars(PublicRow{Days: days[50:], Since: "Monitoring since 24 Oct", SinceDay: 37, StripText: "s"}))
	if strings.Contains(html, "day-bars-since") || strings.Count(html, `data-day="before"`) != 37 {
		t.Errorf("a short gap carries the label:\n%s", html)
	}
	html = renderToString(t, PublicDayBars(PublicRow{Days: days, StripText: "s"}))
	if strings.Contains(html, "before") || strings.Count(html, `data-day="none"`) != 87 {
		t.Errorf("without Since the no-data days stay:\n%s", html)
	}
}
