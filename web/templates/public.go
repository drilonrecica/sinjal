package templates

import "strconv"

// Public status page views (docs/12_STATUS_PAGES.md). They are separate
// from the admin views on purpose: everything here may be read by anyone
// the page lets in, so the web package fills them with public names and
// figures only — never a monitor's own name, target, failure text or id
// beyond what a link needs.

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

// Uptime strip day classes.
const (
	DayUp          = "up"      // 100 % adjusted
	DayPartial     = "partial" // some downtime, at least DayDownBelow
	DayDown        = "down"    // below DayDownBelow
	DayMaintenance = "maint"   // fully covered by excluded maintenance
	DayNoData      = "none"    // before the monitor existed, or paused all day
)

// PublicPage is one rendered status page.
type PublicPage struct {
	Title       string
	Description string
	Logo        string // URL of the logo, or ""
	Overall     string // Overall…
	OverallText string
	Uptime      string // 90-day adjusted uptime of all services, "—" without data
	Groups      []PublicGroup
	Incidents   []PublicIncident
	// IncidentDays is how far back incidents are listed.
	IncidentDays int
	PoweredBy    bool
	Generated    string // when the figures were read, instance time zone
	GeneratedAt  string // RFC 3339
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
	Days      []PublicDay
	StripText string // a text summary of the strip for screen readers
}

// PublicDay is one bar of the uptime strip.
type PublicDay struct {
	Class string // Day…
	Label string // date, uptime and incidents, in words
}

// PublicIncident is an incident of a service on the page.
type PublicIncident struct {
	Service   string
	Active    bool
	Started   string
	StartedAt string
	Duration  string // "so far" while active
	Notes     []PublicNote
}

// PublicNote is a published manual note.
type PublicNote struct {
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

// publicStateGlyph differs in shape per state, so it reads without colour.
func publicStateGlyph(state string) string {
	if state == StateMaintenance {
		return "■"
	}
	return stateGlyph(state)
}

// publicPage is the Page of a public status page: its own theme, never
// the visitor's, and only the public stylesheet.
func publicPage(title, theme string) Page {
	p := NewPage(title, theme, DefaultDensity)
	p.Styles = []string{"css/public.css"}
	return p
}

func groupLabel(g PublicGroup) string {
	if g.Name == "" {
		return "Services"
	}
	return g.Name
}

func overallGlyph(overall string) string {
	switch overall {
	case OverallUp:
		return "●"
	case OverallMajor, OverallPartial:
		return "▼"
	case OverallMaintenance:
		return "■"
	case OverallDegraded:
		return "▲"
	}
	return "○"
}

func pluralDays(n int) string {
	if n == 1 {
		return "day"
	}
	return strconv.Itoa(n) + " days"
}
