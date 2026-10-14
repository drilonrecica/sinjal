package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryIconRenders(t *testing.T) {
	for name := range iconShapes {
		html := renderToString(t, Icon(name))
		if !strings.HasPrefix(html, `<svg class="icon icon-`+name+`"`) || !strings.Contains(html, `aria-hidden="true"`) {
			t.Errorf("%s: %s", name, html)
		}
		if strings.Contains(html, "<style") || strings.Contains(html, "style=") {
			t.Errorf("%s carries inline style, which the CSP blocks", name)
		}
	}
}

func TestUnknownIconPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("unknown icon did not panic")
		}
	}()
	iconShape("no-such-icon")
}

// Every state shown anywhere maps to an icon that exists.
func TestStateIconsExist(t *testing.T) {
	names := []string{}
	for _, s := range []string{StateUp, StateDown, StatePending, StatePaused, StateFlapping, ""} {
		names = append(names, stateIcon(s), problemIcon(s))
	}
	for _, s := range []string{StateMaintenance, StateUp} {
		names = append(names, publicStateIcon(s))
	}
	for _, o := range []string{OverallUp, OverallMajor, OverallPartial, OverallMaintenance, OverallDegraded, ""} {
		names = append(names, overallIcon(o))
	}
	for _, n := range names {
		if _, ok := iconShapes[n]; !ok {
			t.Errorf("icon %q is not in iconShapes", n)
		}
	}
}

// The logo follows the theme: no colour of its own, the pip by class.
func TestLogoHasNoOwnColours(t *testing.T) {
	for name, html := range map[string]string{"mark": renderToString(t, Mark()), "wordmark": renderToString(t, Wordmark())} {
		if strings.Contains(html, "#") || strings.Contains(html, "<style") || strings.Contains(html, "style=") {
			t.Errorf("%s has its own colours or style: %s", name, html)
		}
		if !strings.Contains(html, `class="logo-pip"`) || !strings.Contains(html, `class="logo-ring"`) {
			t.Errorf("%s lacks the ring/pip classes", name)
		}
	}
}

// docs/40: only icons that are used ship. Every name in iconShapes must
// appear quoted in a template or helper outside icons.go and the tests.
func TestEveryShippedIconIsUsed(t *testing.T) {
	var src strings.Builder
	for _, pattern := range []string{"*.templ", "*.go"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if f == "icons.go" || strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "_templ.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(b)
		}
	}
	for name := range iconShapes {
		if !strings.Contains(src.String(), `"`+name+`"`) {
			t.Errorf("icon %q ships but nothing uses it", name)
		}
	}
}
