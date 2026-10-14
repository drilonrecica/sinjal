package templates

import (
	"encoding/json"
	"slices"
)

// Built-in themes and densities (docs/05_THEMES.md, 04_DESIGN_SYSTEM.md).
const (
	DefaultTheme   = "stark"
	DefaultDensity = "comfortable"
)

var (
	themes    = map[string]bool{"stark": true, "terminal": true, "daylight": true}
	densities = map[string]bool{"comfortable": true, "compact": true}
)

// ValidTheme and ValidDensity report whether v is a built-in value.
func ValidTheme(v string) bool   { return themes[v] }
func ValidDensity(v string) bool { return densities[v] }

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
	// AmbientOff switches the Ambient light off (docs/04); the zero value
	// keeps it on, the default.
	AmbientOff bool
	// Login is the signed-in user's name, shown in the account menu.
	Login string
	// View is which half of the shell a phone shows (docs/03 "Phones"):
	// ViewList for the monitor list, ViewDetail (the default) for the pane.
	View string
	// ColumnQuery is the list state (docs/29 "URL state") of the page's own
	// address as a query string; ColumnText and ColumnState are the values
	// the column's search box and chip start from. All "" without one.
	ColumnQuery, ColumnText, ColumnState string
}

// Shell views on phones.
const (
	ViewDetail = "detail"
	ViewList   = "list"
)

// shellView is p.View with the default filled in.
func (p Page) shellView() string {
	if p.View == ViewList {
		return ViewList
	}
	return ViewDetail
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

// Ambient is the data-ambient attribute of <html>: "on" or "off".
func (p Page) Ambient() string {
	if p.AmbientOff {
		return "off"
	}
	return "on"
}

// ColorScheme is the browser color-scheme hint matching the theme, so the
// canvas has the right colour before the stylesheet loads.
func (p Page) ColorScheme() string {
	if p.Theme == "daylight" {
		return "light"
	}
	return "dark"
}

// PrehydrateScript is the one inline script of a page (docs/05 "Theme
// persistence"). On a signed-in page, whose <html> carries data-prefs, it
// remembers the server-rendered theme, density and Ambient light in
// localStorage; on any other page (sign-in, setup) it applies the
// remembered values before first paint, so the sign-in page keeps the look
// of the last session without a flash. Invalid stored values are ignored.
// The Content-Security-Policy admits exactly this text by its hash
// (middleware.ContentSecurityPolicy; a test pins the two together).
const PrehydrateScript = `(function(){try{var d=document.documentElement,k="sinjal.prefs",s=window.localStorage;if(d.hasAttribute("data-prefs")){s.setItem(k,JSON.stringify([d.getAttribute("data-theme"),d.getAttribute("data-density"),d.getAttribute("data-ambient")]));return}var p=JSON.parse(s.getItem(k)||"null");if(!Array.isArray(p))return;if(/^(stark|terminal|daylight)$/.test(p[0])){d.setAttribute("data-theme",p[0]);var m=document.querySelector('meta[name="color-scheme"]');if(m)m.setAttribute("content",p[0]==="daylight"?"light":"dark")}if(p[1]==="comfortable"||p[1]==="compact")d.setAttribute("data-density",p[1]);if(p[2]==="on"||p[2]==="off")d.setAttribute("data-ambient",p[2])}catch(e){}})();`

// SignedIn reports whether the page renders a signed-in user's own
// preferences, which the pre-hydration script then stores.
func (p Page) SignedIn() bool { return p.Login != "" }

// ThemeOption is one choice of the appearance form.
type ThemeOption struct{ Value, Label, Hint string }

// ThemeOptions, DensityOptions list the appearance form's choices.
var (
	ThemeOptions = []ThemeOption{
		{"stark", "Stark", "Pure black, one blue accent."},
		{"terminal", "Terminal", "Monospace, amber on black, box rules."},
		{"daylight", "Daylight", "Warm light, ink for emphasis."},
	}
	DensityOptions = []ThemeOption{
		{"comfortable", "Comfortable", "The default spacing."},
		{"compact", "Compact", "Tighter spacing, more on screen."},
	}
)

// AppearanceForm is the appearance page's view.
type AppearanceForm struct {
	Theme   string
	Density string
	Ambient bool
	Saved   bool
	Error   string
}

// htmxConfig keeps htmx inside the Content-Security-Policy: no injected
// indicator <style>, no eval, no executing <script> in swapped content.
// Loading indicators are styled in the bundled CSS instead. No history
// cache: a cached page is HTML with the inline styles scripts set at run
// time (charts, tooltips), which the policy blocks when it is put back;
// back and forward fetch the page and replace the pane (hx-history-elt).
const htmxConfig = `{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false,"historyCacheSize":0}`

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

// ShellStyles and ShellScripts are every page of the app shell's assets,
// the same on every page. The shell swaps only its pane when it moves
// between pages (htmx boost, docs/03), so a page cannot bring assets of
// its own; one set, cached immutably, is 20 KB of CSS and 53 KB of JS
// gzipped (docs/18). htmx comes first: deferred scripts run in order and
// its extensions need it.
var (
	ShellStyles = []string{
		"css/shell.css", "css/auth.css", "css/monitors.css", "css/monitor_detail.css", "css/monitor_form.css",
		"css/charts.css", "css/uplot.min.css", "css/incidents.css", "css/maintenance.css", "css/notifications.css",
		"css/statuspages.css", "css/settings.css", "css/overview.css", "css/audit.css",
	}
	ShellScripts = []string{
		"js/htmx.min.js", "js/htmx-ext-sse.js", "js/live.js", "js/beats.js", "js/shell.js", "js/palette.js", "js/shortcuts.js",
		"js/uplot.min.js", "js/chart.js", "js/copy.js", "js/passkey.js", "js/toast.js",
	}
)

// withShellAssets replaces the page's assets with the shell's.
func (p Page) withShellAssets() Page {
	p.Styles, p.Scripts = ShellStyles, ShellScripts
	return p
}

// LiveScripts are what a page needs to use Live: the htmx SSE extension,
// the glue that routes events to elements, and the beat bars' tooltip and
// slide-in (every live row and header has a beat bar).
var LiveScripts = []string{"js/htmx-ext-sse.js", "js/live.js", "js/beats.js"}

// Section is one entry of the top bar's navigation (docs/03 "Layout").
// Home is the wordmark and the monitors are the list column, so neither
// is a section of its own.
type Section struct {
	Key   string
	Label string
	Path  string
	Admin bool // shown to admins only: its pages are admin-only
}

// Sections lists the top bar's navigation in display order.
var Sections = []Section{
	{"status-pages", "Status Pages", "/status-pages", true},
	{"incidents", "Incidents", "/incidents", false},
	{"notifications", "Notifications", "/notifications", false},
	{"maintenance", "Maintenance", "/maintenance", false},
	{"settings", "Settings", "/settings/general", false},
}
