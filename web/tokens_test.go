package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var themeNames = []string{"carbon", "paper", "midnight", "terminal"}

// docs/05_THEMES.md "Token model". --space-density-factor is global, not per theme.
var colorTokens = []string{
	"bg", "surface-1", "surface-2", "surface-3", "border", "border-strong",
	"text", "text-muted", "text-subtle", "accent", "accent-contrast",
	"status-up", "status-warning", "status-down", "status-paused", "status-pending", "status-flapping",
	"chart-grid", "chart-line", "chart-outage", "chart-maintenance",
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
	if tokens[":root"] == nil || tokens[":root"]["bg"] != tokens["theme:carbon"]["bg"] {
		t.Error(":root must carry the Carbon defaults so pages without data-theme render")
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
