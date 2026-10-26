package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var themeNames = []string{"stark", "terminal", "daylight"}

// docs/05_THEMES.md "Token model". --space-density-factor is global, not per theme.
var colorTokens = []string{
	"bg", "surface-1", "surface-2", "surface-3", "border", "border-strong",
	"text", "text-muted", "text-subtle", "accent", "accent-contrast",
	"status-up", "status-warning", "status-down", "status-paused", "status-pending", "status-flapping",
	"chart-grid", "chart-line", "chart-outage", "chart-maintenance",
	"on-status", "beat-up", "beat-down", "beat-none", "ambient-up", "ambient-down", "ambient-flapping",
	"calendar-up",
}
var otherTokens = []string{"shadow-1", "shadow-2", "radius-sm", "radius-md", "radius-lg"}

var (
	blockRe   = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	propRe    = regexp.MustCompile(`(?:--)?([a-z0-9-]+)\s*:\s*([^;]+);`)
	commentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	themeSel  = regexp.MustCompile(`\[data-theme="([a-z]+)"\]`)
)

// parseTokens returns, per selector key ("theme:<name>", ":root", "density:compact"), its custom properties.
func parseTokens(t *testing.T) map[string]map[string]string {
	t.Helper()
	raw, err := fs.ReadFile(Static, "css/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := commentRe.ReplaceAllString(string(raw), "")
	out := map[string]map[string]string{}
	for _, m := range blockRe.FindAllStringSubmatch(css, -1) {
		props := map[string]string{}
		for _, p := range propRe.FindAllStringSubmatch(m[2], -1) {
			props[p[1]] = strings.TrimSpace(p[2])
		}
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			key := sel
			if tm := themeSel.FindStringSubmatch(sel); tm != nil {
				key = "theme:" + tm[1]
			} else if strings.HasPrefix(sel, `[data-density="`) {
				key = "density:" + strings.Trim(strings.TrimPrefix(sel, `[data-density=`), `"]`)
			}
			if out[key] == nil {
				out[key] = map[string]string{}
			}
			for k, v := range props {
				out[key][k] = v
			}
		}
	}
	return out
}

func TestEveryThemeDefinesEveryToken(t *testing.T) {
	tokens := parseTokens(t)
	for _, theme := range themeNames {
		props := tokens["theme:"+theme]
		if props == nil {
			t.Errorf("theme %q is not defined", theme)
			continue
		}
		for _, name := range append(append([]string{}, colorTokens...), otherTokens...) {
			if props[name] == "" {
				t.Errorf("theme %q is missing --%s", theme, name)
			}
		}
		if cs := props["color-scheme"]; cs != "light" && cs != "dark" {
			t.Errorf("theme %q has no color-scheme (got %q)", theme, cs)
		}
	}
	if tokens[":root"] == nil || tokens[":root"]["bg"] != tokens["theme:stark"]["bg"] {
		t.Error(":root must carry the Stark defaults so pages without data-theme render")
	}
}

func TestDensityFactor(t *testing.T) {
	tokens := parseTokens(t)
	if got := tokens[":root"]["space-density-factor"]; got != "1" {
		t.Errorf("comfortable factor = %q, want 1", got)
	}
	f, err := strconv.ParseFloat(tokens["density:compact"]["space-density-factor"], 64)
	if err != nil || f <= 0 || f >= 1 {
		t.Errorf("compact factor = %v (%v), want 0 < f < 1", f, err)
	}
}

func TestTerminalHasSharpRadii(t *testing.T) {
	tokens := parseTokens(t)["theme:terminal"]
	for _, name := range []string{"radius-sm", "radius-md", "radius-lg"} {
		px, err := strconv.Atoi(strings.TrimSuffix(tokens[name], "px"))
		if err != nil || px < 2 || px > 4 {
			t.Errorf("terminal --%s = %q, want 2-4px", name, tokens[name])
		}
	}
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(hex) {
		t.Fatalf("colour %q is not #rrggbb", hex)
	}
	ch := func(s string) float64 {
		v, _ := strconv.ParseUint(s, 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(hex[1:3]) + 0.7152*ch(hex[3:5]) + 0.0722*ch(hex[5:7])
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Thresholds follow docs/22_ACCESSIBILITY.md (WCAG 2.2 AA): 4.5:1 for text,
// 3:1 for UI boundaries, chart marks and incidental text.
func TestThemeContrast(t *testing.T) {
	tokens := parseTokens(t)
	surfaces := []string{"bg", "surface-1", "surface-2"}

	for _, theme := range themeNames {
		p := tokens["theme:"+theme]
		check := func(fg, bg string, min float64) {
			t.Helper()
			if got := contrast(t, p[fg], p[bg]); got < min {
				t.Errorf("%s: --%s on --%s = %.2f:1, want >= %.1f:1", theme, fg, bg, got, min)
			}
		}
		for _, s := range append(surfaces, "surface-3") {
			check("text", s, 4.5)
		}
		for _, s := range surfaces {
			check("text-muted", s, 4.5)
			check("accent", s, 4.5)
			for _, st := range []string{"up", "warning", "down", "paused", "pending", "flapping"} {
				check("status-"+st, s, 4.5)
			}
		}
		check("text-subtle", "bg", 3)
		check("text-subtle", "surface-1", 3)
		check("accent-contrast", "accent", 4.5)
		check("border-strong", "bg", 3)
		check("border-strong", "surface-1", 3)
		check("chart-line", "surface-1", 3)
		check("chart-outage", "surface-1", 3)
		check("chart-maintenance", "surface-1", 3)
		// Text on a filled status block, and beat-bar marks.
		for _, st := range []string{"up", "down", "flapping", "warning", "paused", "pending"} {
			check("on-status", "status-"+st, 4.5)
		}
		for _, b := range []string{"beat-up", "beat-down"} {
			check(b, "surface-1", 3)
			check(b, "surface-2", 3)
		}
		// Logbook's calendar cells on their card (docs/04 "Calendar"): the
		// quiet green, and the warning, down and maintenance colours that
		// the other classes use.
		for _, c := range []string{"calendar-up", "status-warning", "status-down", "chart-maintenance"} {
			check(c, "surface-1", 3)
		}
		// Decorative separators only need to be visible, not strong.
		check("border", "bg", 1.15)
		check("chart-grid", "surface-1", 1.05)
	}
}

func TestBaseCSSUsesOnlyTokensForColour(t *testing.T) {
	raw, err := fs.ReadFile(Static, "css/base.css")
	if err != nil {
		t.Fatal(err)
	}
	css := commentRe.ReplaceAllString(string(raw), "")
	if hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(css); hex != "" {
		t.Errorf("base.css hardcodes colour %s; colours must come from tokens.css", hex)
	}
	for _, want := range []string{"prefers-reduced-motion", ":focus-visible", "--space-density-factor"} {
		if !strings.Contains(css, want) {
			t.Errorf("base.css is missing %q", want)
		}
	}
}

// mix is color-mix(in srgb, a p, b) for opaque colours: what a browser
// paints for a tint of a over b.
func mix(t *testing.T, a, b string, p float64) string {
	t.Helper()
	luminance(t, a) // validates the format
	luminance(t, b)
	ch := func(s string, i int) float64 {
		v, _ := strconv.ParseUint(s[i:i+2], 16, 8)
		return float64(v)
	}
	out := "#"
	for _, i := range []int{1, 3, 5} {
		out += hex2(math.Round(ch(a, i)*p + ch(b, i)*(1-p)))
	}
	return out
}

func hex2(v float64) string {
	s := strconv.FormatUint(uint64(v), 16)
	if len(s) == 1 {
		s = "0" + s
	}
	return s
}

// The tints the components paint (components.css), as fractions. A test
// below keeps these in step with the CSS.
const (
	badgeTint        = 0.14 // .status
	noticeTint       = 0.08 // .auth-notice, .auth-alert
	ambientTint      = 0.26 // the Ambient light at its peak
	ambientTintLight = 0.12 // the same in Daylight
	scanlineTint     = 0.12 // Terminal's scanlines
	partialDayTint   = 0.60 // Terminal's day bar for a day with an outage
)

// TestThemeContrastOnPaintedSurfaces checks text where the components lay
// tints over the theme's surfaces (docs/22: 4.5:1 for text, 3:1 for marks).
func TestThemeContrastOnPaintedSurfaces(t *testing.T) {
	tokens := parseTokens(t)
	for _, theme := range themeNames {
		p := tokens["theme:"+theme]
		check := func(what, fg, bg string, min float64) {
			t.Helper()
			if got := contrast(t, fg, bg); got < min {
				t.Errorf("%s: %s = %.2f:1, want >= %.1f:1", theme, what, got, min)
			}
		}
		for _, s := range []string{"bg", "surface-1", "surface-2"} {
			for _, st := range []string{"up", "warning", "down", "paused", "pending", "flapping"} {
				check("--text on the "+st+" badge over --"+s, p["text"], mix(t, p["status-"+st], p[s], badgeTint), 4.5)
			}
		}
		for _, st := range []string{"up", "down"} {
			check("--text on the "+st+" notice", p["text"], mix(t, p["status-"+st], p["surface-1"], noticeTint), 4.5)
		}
		// Terminal draws no Ambient light (TestPaintedMixesMatchCSS keeps it so).
		tint := ambientTint
		if theme == "daylight" {
			tint = ambientTintLight
		}
		for _, a := range []string{"ambient-up", "ambient-down", "ambient-flapping"} {
			if theme == "terminal" {
				break
			}
			lit := mix(t, p[a], p["bg"], tint)
			check("--text in the "+a+" light", p["text"], lit, 4.5)
			check("--text-muted in the "+a+" light", p["text-muted"], lit, 4.5)
		}
		// Daylight's ink block: light text on the ink of --text.
		check("--accent-contrast on an ink block (--text)", p["accent-contrast"], p["text"], 4.5)
		if theme == "terminal" {
			// A day with an outage is the problem colour, dimmed: a mark on
			// the page, and plainly not a clean (--status-up) day.
			partial := mix(t, p["status-down"], p["bg"], partialDayTint)
			check("a partial day on the 90-day bars", partial, p["bg"], 3)
			check("a partial day next to a clean day", p["status-up"], partial, 2)
			for _, fg := range []string{"text", "text-muted"} {
				check("--"+fg+" under a scanline", mix(t, p["bg"], p[fg], scanlineTint), mix(t, p["bg"], p["surface-1"], scanlineTint), 4.5)
			}
		}
		for _, b := range []string{"beat-up", "beat-down"} {
			check("--"+b+" on --bg", p[b], p["bg"], 3)
		}
	}
}

// The fractions above must be the ones components.css paints.
func TestPaintedMixesMatchCSS(t *testing.T) {
	raw, err := fs.ReadFile(Static, "css/components.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, want := range []string{
		"color-mix(in srgb, var(--state) 14%, transparent)",
		"color-mix(in srgb, var(--status-up) 8%, var(--surface-1))",
		"color-mix(in srgb, var(--status-down) 8%, var(--surface-1))",
		"color-mix(in srgb, var(--ambient) 26%, transparent)",
		"color-mix(in srgb, var(--ambient) 12%, transparent)",
		"color-mix(in srgb, var(--bg) 12%, transparent)",
		"[data-theme=\"terminal\"] .day-bars > [data-day=\"partial\"] {\n  background: color-mix(in srgb, var(--status-down) 60%, var(--bg));",
		"[data-theme=\"terminal\"] .monitor-header::before {\n  display: none;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("components.css no longer paints %s; update the tints in this test with it", want)
		}
	}
}

// The tints the status page layouts paint (css/public_<layout>.css), as
// fractions; TestLayoutMixesMatchCSS keeps them in step with the CSS.
const (
	lampHighlight = 0.72 // Signal's bulb, its highlight: the lamp colour over white
	downRowTint   = 0.06 // Heartline's down row
	noteBandTint  = 0.07 // Heartline's ongoing note band
)

// TestStatusPageLayoutContrast checks the colour pairs the four layouts
// add (docs/04 "Status page marks"; docs/22: 4.5:1 for text, 3:1 for
// marks). Pairs already checked above, such as the calendar cells and the
// status colours on the surfaces, are not repeated.
func TestStatusPageLayoutContrast(t *testing.T) {
	tokens := parseTokens(t)
	for _, theme := range themeNames {
		p := tokens["theme:"+theme]
		check := func(what, fg, bg string, min float64) {
			t.Helper()
			if got := contrast(t, fg, bg); got < min {
				t.Errorf("%s: %s = %.2f:1, want >= %.1f:1", theme, what, got, min)
			}
		}
		// A service's state word and glyph, Sentence's verb and Signal's
		// group summary take the state's colour; maintenance is
		// --chart-maintenance, the colour of the lamp, the disc and the dot.
		for _, s := range []string{"bg", "surface-1", "surface-2"} {
			check("the maintenance word on --"+s, p["chart-maintenance"], p[s], 4.5)
		}
		// The lamp (Signal), the disc (Logbook), the mini lamp and the dots
		// in every overall and service colour: a mark on the page and on a
		// card, with the state's glyph inside in --on-status, also where
		// the bulb's highlight lies under it.
		for _, c := range []string{"status-up", "status-down", "status-flapping", "status-paused", "status-pending", "chart-maintenance"} {
			check("--"+c+" lamp or dot on --bg", p[c], p["bg"], 3)
			check("--"+c+" dot on --surface-1", p[c], p["surface-1"], 3)
		}
		for _, c := range []string{"status-up", "status-down", "status-flapping", "status-paused", "chart-maintenance"} {
			check("the glyph on the --"+c+" lamp", p["on-status"], p[c], 3)
			if theme != "terminal" { // Terminal's lamp is a flat block
				check("the glyph on the --"+c+" lamp's highlight", p["on-status"], mix(t, p[c], "#ffffff", lampHighlight), 3)
			}
		}
		// Heartline: a down row is tinted; its words, figure, dashed tail and
		// end dot stay readable on it. The note band is tinted the same way.
		row := mix(t, p["status-down"], p["surface-1"], downRowTint)
		for _, fg := range []string{"text", "text-muted", "status-down"} {
			check("--"+fg+" on a down row", p[fg], row, 4.5)
		}
		check("the down tail on a down row", p["status-down"], row, 3)
		check("the pulse on its panel", p["chart-line"], p["surface-1"], 3)
		band := mix(t, p["status-down"], p["bg"], noteBandTint)
		for _, fg := range []string{"text", "status-down"} {
			check("--"+fg+" on the note band", p[fg], band, 4.5)
		}
		// Logbook: today's ring and the opened day's ring on the card.
		check("today's ring on the calendar card", p["text"], p["surface-1"], 3)
		if theme == "terminal" {
			partial := mix(t, p["status-down"], p["bg"], partialDayTint)
			check("a partial calendar day on its card", partial, p["surface-1"], 3)
		}
	}
}

// The fractions above must be the ones the layout sheets paint.
func TestLayoutMixesMatchCSS(t *testing.T) {
	for file, wants := range map[string][]string{
		"css/public_signal.css":    {"color-mix(in srgb, var(--lamp) 72%, #fff)"},
		"css/public_heartline.css": {"color-mix(in srgb, var(--status-down) 6%, transparent)", "color-mix(in srgb, var(--status-down) 7%, transparent)"},
		"css/public_logbook.css":   {"color-mix(in srgb, var(--status-down) 60%, var(--bg))"},
	} {
		raw, err := fs.ReadFile(Static, file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s no longer paints %s; update the tints in TestStatusPageLayoutContrast with it", file, want)
			}
		}
	}
}
