package templates

import (
	"fmt"
	"strconv"
	"strings"
)

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
	// Names and Verb are the headline (docs/12 "Headline") in two parts,
	// so a layout can colour the verb: "Checkout and API" and "are down"
	// (also "is unstable", "is under maintenance"; three names, then "and
	// n more"). Both are "" when everything is up or nothing is monitored.
	Names string
	Verb  string
	// DownFor is how long the longest ongoing outage has lasted, in words
	// ("18 minutes"), and DownSince the clock time it began ("13:52"); ""
	// without one, or outside an outage.
	DownFor   string
	DownSince string
	// HeroState (up, down, flapping) colours the Ambient light of the
	// layouts that show one.
	HeroState string
	// Note is the newest published note of an ongoing incident, shown
	// in the hero; nil without one.
	Note   *PublicNote
	Uptime string // 90-day adjusted uptime of all services, "—" without data
	// Services counts the page's services and Operational those that are
	// up, for the meta line ("4 of 5 services operational").
	Services    int
	Operational int
	// MedianLatency is the median of the last response times of the
	// services that show latency and have replied ("71 ms"; Heartline's
	// "Response, median"); "" without any.
	MedianLatency string
	Groups        []PublicGroup
	Incidents     []PublicIncident
	// LastIssue is the newest ended incident in words, "The last
	// interruption was 3 days ago, 12 minutes on Web.", or "No
	// interruptions in the last 30 days." without one.
	LastIssue string
	// Calendar is the page's 90 days, oldest first: each day's worst
	// class over the page's services (Logbook). CalendarMonths and
	// CalendarWeeks label its rows (a month per row on desktop, a week on
	// phones; docs/12 "Logbook"), and CalendarUpcoming holds the rest of
	// the current month, drawn dashed.
	Calendar         []PublicDay
	CalendarMonths   []string // "Jul", "Aug", …
	CalendarWeeks    []string // the first date of each week row, "28 Jul"
	CalendarUpcoming []PublicDay
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
	// DownSince is the clock time the service's open incident began
	// ("13:52"); "" while it is not down, or without an open incident.
	DownSince string
	// Pulse is the service's 24-hour response pulse (Heartline); nil when
	// the page does not show its latency, or in another layout.
	Pulse *Pulse

	// Not shown in HTML; for api.json (internal/web/publicfeed.go).
	Key        string // opaque, stable per page; not an id
	LatencyMS  int64
	HasLatency bool
}

// PublicIncident is an incident of a service on the page.
type PublicIncident struct {
	Service   string
	Active    bool
	Date      string // the day it started: "Today", "Yesterday", "Fri 23 Oct"
	Time      string // the clock time it started, "09:14"
	StartedAt string // RFC 3339
	EndTime   string // the clock time it ended, "09:26"; "" while active
	Long      string // how long it was, or has been, down, in words: "12 minutes"
	Short     string // the same in short units, "12 min", "1 h 5 min" (Signal)
	Duration  string // the same in compact units, "12m"; for the feed
	Notes     []PublicNote

	// Not shown in HTML; for api.json and feed.xml.
	Key     string // opaque, stable per page; not an id
	EndedAt string // RFC 3339, "" while active
}

// PublicDay is one day of a page's calendar (docs/12 "Logbook").
type PublicDay struct {
	Class string // the worst Day… of the page's services that day; "" for a day still to come
	Date  string // 2026-10-23, for anchors
	Label string // "Fri 23 Oct"
	Long  string // "Friday 23 October", "Today, Monday 26 October"; the opened day's title
	// Incidents are the indexes in PublicPage.Incidents of the listed
	// incidents that touched the day.
	Incidents []int
	// OthersFine is set when every service without a listed incident that
	// day had no downtime, so the opened day can say so.
	OthersFine bool
	// Where the day sits in the calendar's two grids: the row of its
	// month (0 for the first month of the 90 days) and its day of the
	// month on desktop; the row of its week (0 for the first, weeks start
	// on Monday) and its weekday (Monday 1 … Sunday 7) on phones.
	Month, Dom    int
	Week, Weekday int
}

// Pulse is a service's response over the last 24 hours as SVG paths in a
// fixed 960 × 64 viewBox (docs/04 "Response pulse"): 96 quarter-hour
// buckets, each at its centre, the floor at y 62. Every field but Text may
// be "": a service with no sample in the range has no line, one that is
// up has no tail.
type Pulse struct {
	Line string // the line through the buckets' average response times, broken where a bucket has no sample
	Area string // the area under the line, one subpath per run of buckets
	Tail string // the floor from the moment the service went down to now, drawn dashed
	End  string // the dot at the right end: a zero-length path with round caps
	Text string // "Response over 24 hours: min 41 ms, median 70 ms, max 312 ms"
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

// publicStateTag is Terminal's short tag for a state, shown in place of
// the word ([UP], [DOWN], [FLAP], [MAINT]; docs/05 "Terminal"). The word
// stays for assistive technology.
func publicStateTag(state string) string {
	switch state {
	case StateUp:
		return "UP"
	case StateDown:
		return "DOWN"
	case StatePaused:
		return "PAUSED"
	case StateFlapping:
		return "FLAP"
	case StateMaintenance:
		return "MAINT"
	}
	return "PENDING"
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
// PublicStatusPage.
var builtLayouts = map[string]bool{LayoutSignal: true, LayoutSentence: true, LayoutHeartline: true, LayoutLogbook: true}

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

// pluralDays is "day" or "30 days", for "the last …".
func pluralDays(n int) string {
	if n == 1 {
		return "day"
	}
	return strconv.Itoa(n) + " days"
}

// sentenceSub is the line under an outage headline (docs/12 "Sentence"):
// how long the outage has lasted, and that the rest works, which is left
// out when every service is down.
func sentenceSub(v PublicPage) string {
	var parts []string
	if v.DownFor != "" {
		parts = append(parts, "For "+v.DownFor+" so far.")
	}
	if v.Overall != OverallMajor {
		parts = append(parts, "Everything else is working.")
	}
	return strings.Join(parts, " ")
}

// sentenceMeta follows "Updated 00:57 CET" on Sentence's meta line: calm
// "· 5 services · 99.91% over 90 days", otherwise "· 4 of 5 services
// operational"; nothing when nothing is monitored.
func sentenceMeta(v PublicPage) string {
	switch v.Overall {
	case OverallNone:
		return ""
	case OverallUp:
		return " · " + pluralServices(v.Services) + " · " + v.Uptime + " over 90 days"
	}
	return fmt.Sprintf(" · %d of %s operational", v.Operational, pluralServices(v.Services))
}

// pluralServices is "1 service" or "5 services".
func pluralServices(n int) string {
	if n == 1 {
		return "1 service"
	}
	return strconv.Itoa(n) + " services"
}

// Signal (docs/12 "Signal").

// overallIcon is the glyph inside Signal's lamp, one shape per overall
// state (docs/04 "Status lamp"): the shapes of publicStateIcon, a triangle
// for degraded and a dashed circle when nothing is monitored.
func overallIcon(overall string) string {
	switch overall {
	case OverallUp:
		return "check"
	case OverallMajor, OverallPartial:
		return "octagon-x"
	case OverallDegraded:
		return "triangle-alert"
	case OverallMaintenance:
		return "wrench"
	}
	return "circle-dashed"
}

// signalHeadline is Signal's h1 (docs/12 "Headline"): "All systems
// operational", the services that are down with their verb ("Checkout and
// API are down"), or "No services are monitored".
func signalHeadline(v PublicPage) string {
	switch v.Overall {
	case OverallNone:
		return "No services are monitored"
	case OverallUp:
		return "All systems operational"
	}
	if v.Names == "" {
		return v.OverallText
	}
	return v.Names + " " + v.Verb
}

// signalMeta opens Signal's meta line, before "Updated 00:57 CET": calm
// "5 services · 99.91% uptime over 90 days · ", otherwise "4 of 5
// services operational · "; nothing when nothing is monitored.
func signalMeta(v PublicPage) string {
	switch v.Overall {
	case OverallNone:
		return ""
	case OverallUp:
		return pluralServices(v.Services) + " · " + v.Uptime + " uptime over 90 days · "
	}
	return fmt.Sprintf("%d of %s operational · ", v.Operational, pluralServices(v.Services))
}

// signalLatency follows the state on a service card: " · 71 ms", the
// last check's duration, when the page shows it; " · no response" while
// the service is down; "" when latency is not shown.
func signalLatency(row PublicRow) string {
	switch {
	case row.Latency == "":
		return ""
	case row.State == StateDown:
		return " · no response"
	}
	return " · " + row.Latency
}

// signalBadge is an incident's badge: "Ongoing" or "Resolved".
func signalBadge(active bool) string {
	if active {
		return "Ongoing"
	}
	return "Resolved"
}

// signalGroupSummary sums up a group's services in words, naming the
// worst state: "1 of 3 down", "1 of 3 unstable" (pending or flapping), "1
// of 3 in maintenance"; otherwise "All 3 operational", or "2 of 3
// operational" when one is not monitored. state (StateDown, StateFlapping,
// StateMaintenance or "") colours it.
func signalGroupSummary(g PublicGroup) (text, state string) {
	n := len(g.Rows)
	var down, unstable, maint, up int
	for _, r := range g.Rows {
		switch r.State {
		case StateDown:
			down++
		case StatePending, StateFlapping:
			unstable++
		case StateMaintenance:
			maint++
		case StateUp:
			up++
		}
	}
	switch {
	case down > 0:
		return fmt.Sprintf("%d of %d down", down, n), StateDown
	case unstable > 0:
		return fmt.Sprintf("%d of %d unstable", unstable, n), StateFlapping
	case maint > 0:
		return fmt.Sprintf("%d of %d in maintenance", maint, n), StateMaintenance
	case up == n && n == 1:
		return "Operational", ""
	case up == n:
		return fmt.Sprintf("All %d operational", n), ""
	}
	return fmt.Sprintf("%d of %d operational", up, n), ""
}

// Logbook (docs/12 "Logbook").

// logbookSub opens the line under Logbook's headline, before "Updated
// 14:10 CET.": calm, LastIssue; during an outage "Since 13:52, 18 minutes
// so far." and, while some services are up, "The other 4 services are
// working."; degraded or under maintenance, the latter alone; nothing
// when nothing is monitored.
func logbookSub(v PublicPage) string {
	var parts []string
	switch v.Overall {
	case OverallNone:
		return ""
	case OverallUp:
		return v.LastIssue + " "
	case OverallMajor, OverallPartial:
		if v.DownSince != "" {
			parts = append(parts, "Since "+v.DownSince+", "+v.DownFor+" so far.")
		}
	}
	switch v.Operational {
	case 0:
	case 1:
		parts = append(parts, "The other service is working.")
	default:
		parts = append(parts, fmt.Sprintf("The other %d services are working.", v.Operational))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " "
}

// calendarDoms are the day numbers written over the desktop calendar.
var calendarDoms = []int{1, 5, 10, 15, 20, 25, 30}

// calendarWeekdays head the phone calendar's columns, Monday first.
var calendarWeekdays = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// calendarLegend names each day class, in the legend's order.
var calendarLegend = []struct{ Class, Word string }{
	{DayUp, "No issues"}, {DayPartial, "Some downtime"}, {DayDown, "Outage"}, {DayMaintenance, "Maintenance"}, {DayNoData, "No data"},
}

// calendarWords is a day's date and state in words, for assistive
// technology and the tooltip: "Fri 23 Oct: some downtime, 1 incident". A
// day still to come (Class "") reads "still to come".
func calendarWords(d PublicDay) string {
	state := "still to come"
	for _, l := range calendarLegend {
		if l.Class == d.Class {
			state = strings.ToLower(l.Word)
		}
	}
	switch n := len(d.Incidents); n {
	case 0:
		return d.Label + ": " + state
	case 1:
		return d.Label + ": " + state + ", 1 incident"
	default:
		return fmt.Sprintf("%s: %s, %d incidents", d.Label, state, n)
	}
}

// newestIncidentDay is the index of the last calendar day with a listed
// incident, the day opened without a target; -1 without one.
func newestIncidentDay(cal []PublicDay) int {
	for i := len(cal) - 1; i >= 0; i-- {
		if len(cal[i].Incidents) > 0 {
			return i
		}
	}
	return -1
}

// logbookFigure is a service's uptime in the services list: "99.98% · 90
// days", or "Since 25 Oct, 100.00%" for a service monitored for less;
// "" for one without data.
func logbookFigure(row PublicRow) string {
	switch {
	case row.Uptime == "" || row.Uptime == "—":
		return ""
	case row.Since != "":
		return "Since " + strings.TrimPrefix(row.Since, "Monitoring since ") + ", " + row.Uptime
	}
	return row.Uptime + " · 90 days"
}

// logbookLatency is the last response time where the page shows it, "no
// reply" while the service is down, "" otherwise.
func logbookLatency(row PublicRow) string {
	switch {
	case row.Latency == "":
		return ""
	case row.State == StateDown:
		return "no reply"
	}
	return row.Latency
}

// logbookOrder lists the incidents ongoing first, each group in the
// page's order (newest first).
func logbookOrder(incidents []PublicIncident) []PublicIncident {
	out := make([]PublicIncident, 0, len(incidents))
	for _, in := range incidents {
		if in.Active {
			out = append(out, in)
		}
	}
	for _, in := range incidents {
		if !in.Active {
			out = append(out, in)
		}
	}
	return out
}

// logbookWhen is an entry's time: "Fri 23 Oct · 09:14 to 09:26", or
// "Today · since 13:52" while ongoing.
func logbookWhen(in PublicIncident) string {
	if in.Active {
		return in.Date + " · since " + in.Time
	}
	return in.Date + " · " + in.Time + " to " + in.EndTime
}

// logbookTitle is an entry's sentence: "Scaneks.com was down for 12
// minutes", or "Scaneks.com is down" while ongoing.
func logbookTitle(in PublicIncident) string {
	if in.Active {
		return in.Service + " is down"
	}
	return in.Service + " was down for " + in.Long
}

// Heartline (docs/12 "Heartline").

// heartlineStateSuffix follows the state word on a Heartline row: " since
// 13:52" while the service is down with an open incident; otherwise its
// 90-day uptime, " · 99.98% in 90 days" or " · 100.00% since 25 Oct" for
// a service monitored for less; "" without data.
func heartlineStateSuffix(row PublicRow) string {
	switch {
	case row.State == StateDown && row.DownSince != "":
		return " since " + row.DownSince
	case row.Uptime == "" || row.Uptime == "—":
		return ""
	case row.Since != "":
		return " · " + row.Uptime + " since " + strings.TrimPrefix(row.Since, "Monitoring since ")
	}
	return " · " + row.Uptime + " in 90 days"
}

// heartlineFigure is the current figure beside a pulse and the small line
// under it: "no reply" while the service is down, the last check's time
// ("71 ms"), or a dash for a service without a check yet.
func heartlineFigure(row PublicRow) (figure, sub string) {
	switch {
	case row.State == StateDown:
		return "no reply", "last check"
	case row.Latency == "":
		return "—", "no check yet"
	}
	return row.Latency, "last check"
}

// heartlineDuration is an incident's duration in Heartline's words: "down
// 12 min, resolved" or "down 18 min, ongoing".
func heartlineDuration(in PublicIncident) string {
	if in.Active {
		return "down " + in.Short + ", ongoing"
	}
	return "down " + in.Short + ", resolved"
}
