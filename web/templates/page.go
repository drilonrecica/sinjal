package templates

// Built-in themes and densities (docs/05_THEMES.md, 04_DESIGN_SYSTEM.md).
const (
	DefaultTheme   = "carbon"
	DefaultDensity = "comfortable"
)

var (
	themes    = map[string]bool{"carbon": true, "paper": true, "midnight": true, "terminal": true}
	densities = map[string]bool{"comfortable": true, "compact": true}
)

// Page is the per-request data the layout needs.
type Page struct {
	Title   string
	Theme   string
	Density string
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
