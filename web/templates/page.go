package templates

import (
	"encoding/json"
	"slices"
)

// Built-in themes and densities (docs/05_THEMES.md, 04_DESIGN_SYSTEM.md).
const (
	DefaultTheme   = "carbon"
	DefaultDensity = "comfortable"
)

var (
	themes    = map[string]bool{"carbon": true, "paper": true, "midnight": true, "terminal": true}
	densities = map[string]bool{"comfortable": true, "compact": true}
)

// Page is the per-request data the layout needs. Styles and Scripts are
// logical asset names (for example "css/shell.css") added after the base
// stylesheets; scripts load with defer. CSRFToken is set for signed-in
// requests; forms and htmx requests send it back.
type Page struct {
	Title     string
	Theme     string
	Density   string
	Styles    []string
	Scripts   []string
	CSRFToken string
	Admin     bool // the signed-in user is an admin (settings navigation)
}

// NewPage builds a Page. Unknown theme or density values fall back to the
// defaults, so stored or user-supplied values can never inject arbitrary
// attribute content into <html>.
func NewPage(title, theme, density string) Page {
	if !themes[theme] {
		theme = DefaultTheme
	}
	if !densities[density] {
		density = DefaultDensity
	}
	return Page{Title: title, Theme: theme, Density: density}
}

// ColorScheme is the browser color-scheme hint matching the theme, so the
// canvas has the right colour before the stylesheet loads.
func (p Page) ColorScheme() string {
	if p.Theme == "paper" {
		return "light"
	}
	return "dark"
}

// htmxConfig keeps htmx inside the Content-Security-Policy: no injected
// indicator <style>, no eval, no executing <script> in swapped content.
// Loading indicators are styled in the bundled CSS instead.
const htmxConfig = `{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false}`

// HXHeaders is the hx-headers value that makes htmx send the CSRF token
// on every request. Empty without a token.
func (p Page) HXHeaders() string {
	if p.CSRFToken == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"X-CSRF-Token": p.CSRFToken})
	return string(b)
}

// withAssets returns a copy of p with extra stylesheets and scripts.
func (p Page) withAssets(styles, scripts []string) Page {
	p.Styles = slices.Concat(p.Styles, styles)
	p.Scripts = slices.Concat(p.Scripts, scripts)
	return p
}

// withShellAssets adds the shell stylesheet and htmx. htmx goes before the
// page's own scripts because deferred scripts run in order and extensions
// need htmx to exist.
func (p Page) withShellAssets() Page {
	p.Styles = slices.Concat(p.Styles, []string{"css/shell.css"})
	p.Scripts = slices.Concat([]string{"js/htmx.min.js"}, p.Scripts)
	return p
}

// LiveScripts are what a page needs to use Live: the htmx SSE extension and
// the glue that routes events to elements.
var LiveScripts = []string{"js/htmx-ext-sse.js", "js/live.js"}

// Section is one entry of the primary navigation (docs/03_INFORMATION_ARCHITECTURE.md).
type Section struct {
	Key   string
	Label string
	Path  string
}

// Sections lists the primary navigation in display order.
var Sections = []Section{
	{"overview", "Overview", "/"},
	{"monitors", "Monitors", "/monitors"},
	{"incidents", "Incidents", "/incidents"},
	{"status-pages", "Status Pages", "/status-pages"},
	{"notifications", "Notifications", "/notifications"},
	{"maintenance", "Maintenance", "/maintenance"},
	{"settings", "Settings", "/settings/general"},
}
