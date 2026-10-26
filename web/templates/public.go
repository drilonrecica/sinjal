package templates

import "strconv"

// Public status page views (docs/12_STATUS_PAGES.md). They are separate
// from the admin views on purpose: everything here may be read by anyone
// the page lets in, so the web package fills them with public names and
// figures only — never a monitor's own name, target, failure text or id
// beyond what a link needs.

// PublicRefreshSeconds is how often a status page reloads itself (docs/12,
// docs/32). It matches the 10 s server cache closely enough that a visitor
// sees a change within about a minute.
const PublicRefreshSeconds = 60

// Overall states of a public page, worst first.
const (
	OverallMajor       = "major"       // every service is down
	OverallPartial     = "partial"     // some services are down
	OverallDegraded    = "degraded"    // pending or flapping
	OverallMaintenance = "maintenance" // in a maintenance window
	OverallUp          = "up"
	OverallNone        = "none" // no services, or all paused
)

// Public service states besides the monitor states (StateUp, …).
const StateMaintenance = "maintenance"

// PublicPage is one rendered status page.
type PublicPage struct {
	// Layout is the page's layout (statuspage.Layouts); an unknown or
	// empty one renders as the default, Sentence (publicLayout).
	Layout      string
	Title       string
	Description string
	Logo        string // URL of the logo, or ""
	Overall     string // Overall…
	OverallText string
	// Sentence is the hero (M8-17): OverallText as a sentence, naming the
	// services that are down; HeroState (up, down, flapping) colours its
	// Ambient light.
	Sentence  string
	HeroState string
	// Note is the newest published note of an ongoing incident, shown
	// under the sentence; nil without one.
	Note      *PublicNote
	Uptime    string // 90-day adjusted uptime of all services, "—" without data
	Groups    []PublicGroup
	Incidents []PublicIncident
	// LastIssue is the newest ended incident in words, "The last
	// interruption was 3 days ago, 12 minutes on Web.", or "No
	// interruptions in the last 30 days." without one.
	LastIssue string
	// Calendar is the page's 90 days, oldest first: each day's worst
	// class over the page's services (Logbook).
	Calendar []PublicDay
	// FeedURL is the page's feed.xml under the address it is served at
	// (/status/{slug}, /s/{token} or a mapped hostname's /). The view is
	// shared by those addresses, so it is set per request, not cached.
	FeedURL string
	// IncidentDays is how far back incidents are listed.
	IncidentDays int
	PoweredBy    bool
	Generated    string // when the figures were read, instance time zone
	GeneratedAt  string // RFC 3339
	Zone         string // the instance time zone's abbreviation, "CET"
	// AccentCSS is the custom accent as CSS (from a checked #rrggbb only),
	// or "". The response's CSP allows exactly this text by its hash.
	AccentCSS string
	NoIndex   bool // unlisted pages ask search engines to stay away
}

// PublicGroup is a named group of services; Name is "" for the services
// outside any group.
type PublicGroup struct {
	Name string
	Rows []PublicRow
}

// PublicRow is one service: a monitor under its public name.
type PublicRow struct {
	Name      string
	State     string // StateUp, StateDown, StatePending, StatePaused, StateFlapping or StateMaintenance
	Latency   string // "128 ms", or "" when not shown
	Uptime    string // 90-day adjusted
	Days      []Day
	StripText string // a text summary of the strip for screen readers
	// Since is "Monitoring since 25 Oct" when the 90 days start before
	// the service's first day with data, and SinceDay that day's index;
	// "" and 0 otherwise. The strip leaves out the days before it.
	Since    string
	SinceDay int
	// Downtime is the 90-day adjusted downtime in words: "Down 26 min in
	// 90 days", "No downtime since 25 Oct", "Down now, for 18 min"; ""
	// without any data.
	Downtime string

	// Not shown in HTML; for api.json (internal/web/publicfeed.go).
	Key        string // opaque, stable per page; not an id
	LatencyMS  int64
	HasLatency bool
}

// PublicIncident is an incident of a service on the page.
type PublicIncident struct {
	Service   string
	Active    bool
	Started   string
	StartedAt string
	Duration  string // "so far" while active
	Notes     []PublicNote

	// Not shown in HTML; for api.json and feed.xml.
	Key     string // opaque, stable per page; not an id
	EndedAt string // RFC 3339, "" while active
}

// PublicDay is one day of a page's calendar.
type PublicDay struct {
	Class string // the worst Day… of the page's services that day
	Date  string // 2026-10-23, for anchors
	Label string // "Fri 23 Oct"
	// Incidents are the indexes in PublicPage.Incidents of the listed
	// incidents that touched the day.
	Incidents []int
}

// PublicNote is a published manual note. Service is set for the note
// under the sentence, which stands apart from its incident.
type PublicNote struct {
	Service string
	Message string
	Time    string
	At      string
}

// PublicPasswordView is the form that opens a password-protected page.
// Action is the page's own address; CSRFToken is set for signed-in
// visitors, whose posts need it.
type PublicPasswordView struct {
	Title     string
	Action    string
	Error     string
	CSRFToken string
}

// publicStateLabel is the visible state of a service.
func publicStateLabel(state string) string {
	switch state {
	case StateUp:
		return "Operational"
	case StateDown:
		return "Down"
	case StatePaused:
		return "Not monitored"
	case StateFlapping:
		return "Unstable"
	case StateMaintenance:
		return "Maintenance"
	}
	return "Checking"
}

// publicStateIcon differs in shape per state, so it reads without colour.
func publicStateIcon(state string) string {
	if state == StateMaintenance {
		return "wrench"
	}
	return stateIcon(state)
}

// publicPage is the page a status page renders in: its own theme, or
// Daylight, the status page default (docs/05), for an unknown one; never
// the visitor's. layout adds that layout's stylesheet to the shared one;
// "" is a page without a layout (the password form, a message).
func publicPage(title, theme, layout string) Page {
	if !themes[theme] {
		theme = PublicDefaultTheme
	}
	p := NewPage(title, theme, DefaultDensity)
	p.Styles = []string{"css/public.css"}
	if layout != "" {
		p.Styles = append(p.Styles, "css/public_"+layout+".css")
	}
	return p
}

// Layouts of a status page (statuspage.Layouts).
const (
	LayoutSignal    = "signal"
	LayoutSentence  = "sentence"
	LayoutHeartline = "heartline"
	LayoutLogbook   = "logbook"
)

// builtLayouts are the layouts with a component of their own in
// PublicStatusPage. SL-05 to SL-07 add the others.
var builtLayouts = map[string]bool{LayoutSentence: true}

// publicLayout is the layout a page renders in: its own when that one is
// built, Sentence otherwise, as for an unknown or empty layout (docs/12
// "Default and migration").
func publicLayout(layout string) string {
	if builtLayouts[layout] {
		return layout
	}
	return LayoutSentence
}

// PublicDefaultTheme is a status page's theme when it has none it knows.
const PublicDefaultTheme = "daylight"

// sinceLabelDays is the fewest days before a strip's first day with data
// that leave room for the "Monitoring since" label over them, at 390 px
// too; with fewer the dashed line stands alone and the row's Downtime and
// the strip's label still name the day.
const sinceLabelDays = 40

func sinceText(row PublicRow) string {
	if row.SinceDay < sinceLabelDays {
		return ""
	}
	return row.Since
}

func groupLabel(g PublicGroup) string {
	if g.Name == "" {
		return "Services"
	}
	return g.Name
}

func overallIcon(overall string) string {
	switch overall {
	case OverallUp:
		return "check"
	case OverallMajor, OverallPartial:
		return "octagon-x"
	case OverallMaintenance:
		return "wrench"
	case OverallDegraded:
		return "triangle-alert"
	}
	return "circle-dashed"
}

func pluralDays(n int) string {
	if n == 1 {
		return "day"
	}
	return strconv.Itoa(n) + " days"
}
