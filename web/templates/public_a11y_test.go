package templates

import (
	"regexp"
	"strings"
	"testing"
)

// SL-08, docs/22: every layout in every theme has one h1 first and no
// skipped heading level, the banner, main and contentinfo landmarks once
// each, a name on every section, and a glyph per service state whose shape
// differs per state.

var (
	headingRe  = regexp.MustCompile(`<h([1-6])[\s>]`)
	sectionRe  = regexp.MustCompile(`<section\b[^>]*>`)
	labelledRe = regexp.MustCompile(`aria-labelledby="([^"]+)"`)
	// A service row and the glyph of its state.
	rowGlyphRe = regexp.MustCompile(`(?s)<li class="[a-z-]+" data-state="([a-z]+)"[^>]*>.*?<span class="public-glyph"><svg class="icon icon-([a-z-]+)"`)
)

var publicStates = []string{StateUp, StateDown, StateFlapping, StatePending, StatePaused, StateMaintenance}

// everyState is a page of each layout with a group holding one service in
// every state, in an outage with a note, so each layout renders its every
// part: Heartline's pulse, Logbook's calendar and panel, Signal's lamp.
func everyState(layout string) PublicPage {
	v := heartlineCalm()
	cal := logbookCalm()
	v.Calendar, v.CalendarMonths, v.CalendarWeeks, v.CalendarUpcoming, v.Incidents = cal.Calendar, cal.CalendarMonths, cal.CalendarWeeks, cal.CalendarUpcoming, cal.Incidents
	v.Layout, v.Overall, v.HeroState, v.Names, v.Verb = layout, OverallPartial, StateDown, "Down service", "is down"
	v.Note = &PublicNote{Service: "Down service", Message: "We are on it.", Time: "14:05 CET", At: "2026-10-26T14:05:00+01:00"}
	g := PublicGroup{Name: "Every state"}
	for _, s := range publicStates {
		g.Rows = append(g.Rows, PublicRow{Name: s + " service", State: s, Uptime: "99.00%", Pulse: v.Groups[0].Rows[0].Pulse})
	}
	v.Groups = append(v.Groups, g)
	return v
}

func TestPublicLayoutsHeadingsAndLandmarks(t *testing.T) {
	for _, layout := range []string{LayoutSignal, LayoutSentence, LayoutHeartline, LayoutLogbook} {
		for _, theme := range []string{"daylight", "stark", "terminal"} {
			html := renderToString(t, PublicStatusPage(everyState(layout), theme))
			name := layout + "/" + theme
			levels := headingRe.FindAllStringSubmatch(html, -1)
			if len(levels) == 0 || levels[0][1] != "1" {
				t.Fatalf("%s: the first heading is not the h1: %v", name, levels)
			}
			prev := 1
			for _, l := range levels[1:] {
				n := int(l[1][0] - '0')
				if n == 1 {
					t.Errorf("%s: a second h1", name)
				}
				if n > prev+1 {
					t.Errorf("%s: h%d follows h%d", name, n, prev)
				}
				prev = n
			}
			for _, landmark := range []string{"<header ", "<main>", "<footer "} {
				if n := strings.Count(html, landmark); n != 1 {
					t.Errorf("%s: %d %s landmarks, want 1", name, n, landmark)
				}
			}
			if strings.Index(html, `<h1 `) < strings.Index(html, "<main>") {
				t.Errorf("%s: the h1 is outside main", name)
			}
			for _, s := range sectionRe.FindAllString(html, -1) {
				if strings.Contains(s, "aria-label=") {
					continue
				}
				m := labelledRe.FindStringSubmatch(s)
				if m == nil || !strings.Contains(html, `id="`+m[1]+`"`) {
					t.Errorf("%s: %s has no name", name, s)
				}
			}
		}
	}
}

func TestPublicLayoutsGlyphPerState(t *testing.T) {
	shapes := map[string]string{}
	for _, s := range publicStates {
		shape := iconShape(publicStateIcon(s))
		for other, o := range shapes {
			if o == shape {
				t.Errorf("%s and %s share a glyph", s, other)
			}
		}
		shapes[s] = shape
	}
	for _, layout := range []string{LayoutSignal, LayoutSentence, LayoutHeartline, LayoutLogbook} {
		v := everyState(layout)
		rows := 0
		for _, g := range v.Groups {
			rows += len(g.Rows)
		}
		html := renderToString(t, PublicStatusPage(v, "terminal"))
		found := rowGlyphRe.FindAllStringSubmatch(html, -1)
		if len(found) != rows || strings.Count(html, `<span class="public-glyph">`) != rows {
			t.Errorf("%s: %d glyphs for %d services", layout, len(found), rows)
		}
		for _, f := range found {
			if want := publicStateIcon(f[1]); f[2] != want {
				t.Errorf("%s: a %s service shows the %s glyph, want %s", layout, f[1], f[2], want)
			}
		}
	}
}
