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
		if !strings.Contains(html, `<div class="public-page" data-layout="`+want+`" data-overall="up">`) {
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
		`<p class="public-title-name">Acme</p><p class="public-description">What we run</p>`,
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
	// The sentence is the page's one h1; the title in the header is not a
	// heading, so the heading order starts at the status.
	if strings.Count(html, "<h1") != 1 {
		t.Errorf("%d h1 elements", strings.Count(html, "<h1"))
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

// sentenceCalm is a Sentence page with everything working.
var sentenceCalm = PublicPage{Layout: LayoutSentence, Title: "Laser Pickle", Overall: OverallUp, OverallText: "All systems operational",
	Uptime: "99.91%", Services: 5, Operational: 5, Generated: "00:57 CET", GeneratedAt: "2026-10-26T00:57:00+01:00",
	IncidentDays: 30, Zone: "CET", LastIssue: "The last interruption was 3 days ago, 12 minutes on Scaneks.com.",
	Groups: []PublicGroup{
		{Name: "POS Kosova", Rows: []PublicRow{
			{Name: "Scaneks.com", State: StateUp, Days: make([]Day, 90), Downtime: "Down 6 h 16 min in 90 days", Uptime: "99.70%", Latency: "71 ms"},
			{Name: "Checkout", State: StateFlapping, Days: make([]Day, 90), Downtime: "Down 26 min in 90 days"}}},
		{Rows: []PublicRow{{Name: "Recica.dev", State: StateMaintenance, Days: make([]Day, 90), Downtime: "No downtime in 90 days"}}},
	},
	Incidents: []PublicIncident{{Service: "Scaneks.com", Date: "Fri 23 Oct", Time: "09:14", StartedAt: "2026-10-23T09:14:00+02:00", Long: "12 minutes", Duration: "12m",
		Notes: []PublicNote{{Message: "A slow database query blocked the shop. It is fixed.", Time: "09:31 CET", At: "2026-10-23T09:31:00+02:00"}}}},
}

// The Sentence layout (docs/12 "Sentence"), calm: the top rule in the up
// colour, the editorial sentence with LastIssue under it, the meta line,
// groups as labels, each service as name, state and downtime, and the
// incidents as sentences with their notes as quotes.
func TestSentencePageCalm(t *testing.T) {
	html := renderToString(t, PublicStatusPage(sentenceCalm, "daylight"))
	for _, want := range []string{
		`<div class="public-page" data-layout="sentence" data-overall="up">`,
		`<h1 id="overall">Everything at Laser Pickle is working.</h1>`,
		`<p class="sentence-sub">The last interruption was 3 days ago, 12 minutes on Scaneks.com.</p>`,
		`<p class="sentence-meta">Updated <time datetime="2026-10-26T00:57:00+01:00">00:57 CET</time> · 5 services · 99.91% over 90 days</p>`,
		`<h2 class="public-group-name">POS Kosova</h2>`,
		`<h2 class="public-group-name">Services</h2>`,
		`<span class="public-service-name">Scaneks.com</span>`,
		`<span class="status-text">Operational</span><span class="status-tag" aria-hidden="true">UP</span>`,
		`<span class="status-text">Unstable</span><span class="status-tag" aria-hidden="true">FLAP</span>`,
		`<span class="status-text">Maintenance</span><span class="status-tag" aria-hidden="true">MAINT</span>`,
		`<span class="public-service-downtime">Down 6 h 16 min in 90 days</span>`,
		`<h2 class="public-group-name" id="incidents">The last 30 days</h2>`,
		`<time datetime="2026-10-23T09:14:00+02:00">Fri 23 Oct</time>`,
		`Scaneks.com was down for 12 minutes, from <time datetime="2026-10-23T09:14:00+02:00">09:14</time>.`,
		`<q>A slow database query blocked the shop. It is fixed.</q> <time datetime="2026-10-23T09:31:00+02:00">09:31 CET</time>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// Sentence leaves out latency and the per-service uptime figure, and
	// the M8-17 markup is gone.
	for _, leak := range []string{"71 ms", "99.70%", "public-hero", "public-overall-", "uptime over 90 days", "Ongoing", "Resolved", "sentence-verb", "sentence-quote"} {
		if strings.Contains(html, leak) {
			t.Errorf("calm page shows %s", leak)
		}
	}
	// One h1, the sentence; the groups and the incidents are h2.
	if strings.Count(html, "<h1") != 1 || strings.Count(html, "<h2") != 3 {
		t.Errorf("%d h1 and %d h2", strings.Count(html, "<h1"), strings.Count(html, "<h2"))
	}
	// Without incidents the section says so.
	v := sentenceCalm
	v.Incidents = nil
	if html := renderToString(t, PublicStatusPage(v, "daylight")); !strings.Contains(html, `<p class="public-quiet">No incidents in the last 30 days.</p>`) {
		t.Error("no quiet line without incidents")
	}
}

// Sentence during an outage: the down services by name with the verb in
// the state colour, the time so far, "Everything else is working.", the
// ongoing note as a quote with its time, and the meta line counts the
// services that are up.
func TestSentencePageOutage(t *testing.T) {
	v := sentenceCalm
	v.Overall, v.OverallText, v.Names, v.Verb, v.DownFor, v.Operational = OverallPartial, "Partial outage", "Scaneks.com", "is down", "18 minutes", 4
	v.Generated, v.GeneratedAt = "14:10 CET", "2026-10-26T14:10:00+01:00"
	v.Note = &PublicNote{Service: "Scaneks.com", Message: "Our hosting provider has network trouble.", Time: "14:05 CET", At: "2026-10-26T14:05:00+01:00"}
	v.Groups[0].Rows[0].State, v.Groups[0].Rows[0].Downtime = StateDown, "Down now, for 18 min"
	v.Incidents = append([]PublicIncident{{Service: "Scaneks.com", Active: true, Date: "Today", Time: "13:52", StartedAt: "2026-10-26T13:52:00+01:00", Long: "18 minutes", Duration: "18m",
		Notes: []PublicNote{{Message: "Our hosting provider has network trouble.", Time: "14:05 CET", At: "2026-10-26T14:05:00+01:00"}}}}, v.Incidents...)
	html := renderToString(t, PublicStatusPage(v, "stark"))
	for _, want := range []string{
		`<div class="public-page" data-layout="sentence" data-overall="partial">`,
		`<h1 id="overall">Scaneks.com <span class="sentence-verb">is down.</span></h1>`,
		`<p class="sentence-sub">For 18 minutes so far. Everything else is working.</p>`,
		`<blockquote class="public-hero-note"><p>Our hosting provider has network trouble.</p><p class="public-hero-note-meta">Update at <time datetime="2026-10-26T14:05:00+01:00">14:05 CET</time> · Scaneks.com</p></blockquote>`,
		`<p class="sentence-meta">Updated <time datetime="2026-10-26T14:10:00+01:00">14:10 CET</time> · 4 of 5 services operational</p>`,
		`<li class="public-service" data-state="down">`,
		`<span class="status-text">Down</span><span class="status-tag" aria-hidden="true">DOWN</span>`,
		`<li class="public-incident" data-active>`,
		`<time datetime="2026-10-26T13:52:00+01:00">Today</time>`,
		`Scaneks.com has been down since <time datetime="2026-10-26T13:52:00+01:00">13:52</time>.`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("outage page lacks %s", want)
		}
	}
	if strings.Contains(html, "The last interruption") || strings.Contains(html, "99.91%") {
		t.Error("an outage page shows LastIssue or the uptime figure")
	}
	// Every service down: no "Everything else is working.", and no time
	// without an incident to take it from.
	v.Overall, v.Names, v.Verb, v.DownFor, v.Operational = OverallMajor, "Scaneks.com, Checkout and Recica.dev", "are down", "", 0
	html = renderToString(t, PublicStatusPage(v, "terminal"))
	if !strings.Contains(html, `<h1 id="overall">Scaneks.com, Checkout and Recica.dev <span class="sentence-verb">are down.</span></h1>`) ||
		strings.Contains(html, "sentence-sub") || !strings.Contains(html, "0 of 5 services operational") {
		t.Errorf("major outage:\n%s", html)
	}
	// Degraded and maintenance follow the outage form.
	v.Overall, v.Names, v.Verb, v.Operational = OverallMaintenance, "Recica.dev", "is under maintenance", 4
	if html := renderToString(t, PublicStatusPage(v, "daylight")); !strings.Contains(html, `Recica.dev <span class="sentence-verb">is under maintenance.</span></h1>`) ||
		!strings.Contains(html, `<p class="sentence-sub">Everything else is working.</p>`) {
		t.Errorf("maintenance:\n%s", html)
	}
	// Nothing monitored.
	v.Overall, v.Names, v.Verb, v.Services, v.Operational, v.Groups = OverallNone, "", "", 0, 0, nil
	if html := renderToString(t, PublicStatusPage(v, "daylight")); !strings.Contains(html, `<h1 id="overall">Nothing is monitored on this page yet.</h1>`) ||
		!strings.Contains(html, `<p class="sentence-meta">Updated <time datetime="2026-10-26T14:10:00+01:00">14:10 CET</time></p>`) || strings.Contains(html, "sentence-sub") {
		t.Errorf("none:\n%s", html)
	}
	// A single service and a single incident day read in the singular.
	v.Overall, v.Services, v.Operational, v.IncidentDays, v.Incidents = OverallUp, 1, 1, 1, nil
	if html := renderToString(t, PublicStatusPage(v, "daylight")); !strings.Contains(html, "· 1 service ·") || !strings.Contains(html, "The last day</h2>") {
		t.Errorf("singular:\n%s", html)
	}
}

// Every public state has a Terminal tag beside its word.
func TestPublicStateTag(t *testing.T) {
	for state, want := range map[string]string{StateUp: "UP", StateDown: "DOWN", StatePending: "PENDING", StatePaused: "PAUSED", StateFlapping: "FLAP", StateMaintenance: "MAINT"} {
		if got := publicStateTag(state); got != want {
			t.Errorf("publicStateTag(%s) = %q, want %q", state, got, want)
		}
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
