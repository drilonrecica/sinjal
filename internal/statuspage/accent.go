package statuspage

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Surface colours of the page behind the accent, by theme: --bg and
// --surface-1 of web/static/css/tokens.css (a test keeps them in step).
var surfaces = map[string][2]string{
	"carbon":   {"#15171a", "#1b1e22"},
	"paper":    {"#f5f2eb", "#fffdf8"},
	"midnight": {"#0b1020", "#11182b"},
	"terminal": {"#10120f", "#151815"},
}

// MinAccentContrast is the WCAG contrast the accent needs against the
// page, since it colours links and text (docs/22_ACCESSIBILITY.md).
const MinAccentContrast = 4.5

var accentRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// NormalizeAccent trims and lowercases an accent given as #rrggbb. Empty
// means the theme's own accent.
func NormalizeAccent(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, s == "" || accentRe.MatchString(s)
}

// CheckAccent reports why accent (normalized, non-empty) cannot be read on
// the theme, or "" when it can.
func CheckAccent(accent, theme string) string {
	s, ok := surfaces[theme]
	if !ok {
		return ""
	}
	worst := 21.0
	for _, bg := range s {
		worst = min(worst, Contrast(accent, bg))
	}
	if worst < MinAccentContrast {
		return fmt.Sprintf("Too little contrast on the %s theme (%.1f:1, at least %.1f:1 is needed to read it): choose a lighter or darker colour.", theme, worst, MinAccentContrast)
	}
	return ""
}

// Contrast is the WCAG contrast ratio of two #rrggbb colours.
func Contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return 0
	}
	channel := func(c uint64) float64 {
		x := float64(c) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(v>>16&0xff) + 0.7152*channel(v>>8&0xff) + 0.0722*channel(v&0xff)
}
