package templates

import (
	"strings"

	"github.com/drilonrecica/sinjal/internal/statuspage"
)

// StatusPageListView is the Status Pages page.
type StatusPageListView struct {
	Rows []StatusPageRow
}

// StatusPageRow is one page in the list.
type StatusPageRow struct {
	ID         string
	Title      string
	Slug       string
	Visibility string // a statuspage visibility value
	Address    string // where it is served, "" for an unlisted page
	Monitors   int
	Hosts      []string
}

// VisibilityLabel is the name of a visibility mode.
func VisibilityLabel(v string) string {
	switch v {
	case statuspage.Public:
		return "Public"
	case statuspage.Authenticated:
		return "Signed-in users"
	case statuspage.Password:
		return "Password"
	case statuspage.Unlisted:
		return "Unlisted"
	}
	return v
}

// visibilityHint says what each mode means (docs/12 "Visibility modes").
func visibilityHint(v string) string {
	switch v {
	case statuspage.Public:
		return "Anyone with the address can open it."
	case statuspage.Authenticated:
		return "Only people signed in to Sinjal, as admin or viewer."
	case statuspage.Password:
		return "Visitors enter a password for this page; no Sinjal account is needed."
	}
	return "Served at a secret address that is shown once and linked from nowhere. Not a substitute for a password when the content is sensitive."
}

// StatusPageForm is the state of the create/edit page. Values are kept as
// typed so a rejected one is shown back as entered; the password is never
// shown back.
type StatusPageForm struct {
	ID            string // "" while creating
	Title         string
	Slug          string
	Description   string
	Visibility    string
	Theme         string
	Accent        string
	IncidentDays  string
	ShowPoweredBy bool
	HasPassword   bool   // a password is already stored
	HasToken      bool   // an unlisted address was already issued
	Groups        string // one name per line
	GroupNames    []string
	Hosts         string // one hostname per line
	Monitors      []StatusPageMonitorRow
	Origin        string // the instance's own address, for the preview of the page address
	// Errors maps a field name to its message; "form" holds a failure not
	// tied to one field, "m_name_<id>" and "m_group_<id>" those of a monitor.
	Errors map[string]string
}

// StatusPageMonitorRow is one monitor of the instance and how the page
// shows it.
type StatusPageMonitorRow struct {
	ID          string
	Name        string // the monitor's own name, for the admin only
	Show        bool
	DisplayName string
	Group       string
	Order       string
	ShowLatency bool
}

// Editing reports whether the form edits an existing page.
func (f StatusPageForm) Editing() bool { return f.ID != "" }

// Action is where the form posts.
func (f StatusPageForm) Action() string {
	if f.Editing() {
		return "/status-pages/" + f.ID
	}
	return "/status-pages"
}

// VisibilityOptions lists the modes with their labels.
func (StatusPageForm) VisibilityOptions() []Option {
	var out []Option
	for _, v := range statuspage.Visibilities {
		out = append(out, Option{v, VisibilityLabel(v)})
	}
	return out
}

// ThemeOptions lists the built-in themes.
func (StatusPageForm) ThemeOptions() []Option {
	var out []Option
	for _, t := range statuspage.Themes {
		out = append(out, Option{t, strings.ToUpper(t[:1]) + t[1:]})
	}
	return out
}

var statusPageFieldOrder = []string{"title", "slug", "description", "visibility", "password", "theme", "accent",
	"incident_days", "groups", "monitors", "hosts"}

var statusPageLabels = map[string]string{
	"title": "Title", "slug": "Address", "description": "Description", "visibility": "Who can see it",
	"password": "Page password", "theme": "Theme", "accent": "Accent colour", "incident_days": "Incident history",
	"groups": "Groups", "monitors": "Monitors", "hosts": "Hostnames",
}

// summary lists the errors in page order, the monitors' own last.
func (f StatusPageForm) summary() []summaryItem {
	var out []summaryItem
	for _, k := range statusPageFieldOrder {
		if msg, ok := f.Errors[k]; ok {
			out = append(out, summaryItem{k, statusPageLabels[k], msg})
		}
	}
	for _, m := range f.Monitors {
		for _, k := range []string{"m_name_" + m.ID, "m_group_" + m.ID, "m_order_" + m.ID} {
			if msg, ok := f.Errors[k]; ok {
				out = append(out, summaryItem{k, "Monitor " + m.Name, msg})
			}
		}
	}
	return out
}

// StatusPageTokenView is the one-time view of an unlisted page's address.
type StatusPageTokenView struct {
	ID          string
	Title       string
	URL         string
	Regenerated bool // a new address replaced an old one
}

// PageAddress is where a page is served, "" when it has no fixed address.
func PageAddress(slug, visibility string) string {
	if visibility == statuspage.Unlisted {
		return ""
	}
	return "/status/" + slug
}
