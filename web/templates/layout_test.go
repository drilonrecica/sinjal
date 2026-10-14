package templates

import (
	"bytes"
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/drilonrecica/sinjal/internal/assets"
)

// renderWith renders c with child as its children.
func renderWith(t *testing.T, c templ.Component, child templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	ctx := templ.WithChildren(context.Background(), child)
	if err := c.Render(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestLayoutRendersDocument(t *testing.T) {
	out := renderWith(t, Layout(NewPage("Overview", "", "")), templ.Raw("<p>hello</p>"))

	for _, want := range []string{
		"<!doctype html>", `<html lang="en"`, `<meta charset="utf-8">`,
		`name="viewport"`, "<title>Overview</title>",
	} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	body := out[strings.Index(out, "<body>"):]
	if !strings.Contains(body, "<p>hello</p>") {
		t.Errorf("children were not rendered inside <body>:\n%s", out)
	}
}

func TestLayoutEscapesTitle(t *testing.T) {
	out := renderWith(t, Layout(NewPage(`<script>alert(1)</script>`, "", "")), templ.NopComponent)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("title was not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected an escaped title:\n%s", out)
	}
}

func TestLayoutThemeAndDensityAttributes(t *testing.T) {
	tests := []struct {
		theme, density                     string
		wantTheme, wantDensity, wantScheme string
	}{
		{"", "", "stark", "comfortable", "dark"},
		{"daylight", "compact", "daylight", "compact", "light"},
		{"carbon", "comfortable", "stark", "comfortable", "dark"}, // a removed theme falls back
		{"terminal", "compact", "terminal", "compact", "dark"},
		{"neon", "huge", "stark", "comfortable", "dark"},
		{`daylight" onload="alert(1)`, `x"><script>`, "stark", "comfortable", "dark"},
	}
	for _, tt := range tests {
		out := renderWith(t, Layout(NewPage("T", tt.theme, tt.density)), templ.NopComponent)
		for _, want := range []string{
			`data-theme="` + tt.wantTheme + `"`,
			`data-density="` + tt.wantDensity + `"`,
			`<meta name="color-scheme" content="` + tt.wantScheme + `">`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("theme=%q density=%q: missing %s in\n%s", tt.theme, tt.density, want, out)
			}
		}
		if bare := strings.Replace(out, "<script>"+PrehydrateScript+"</script>", "", 1); strings.Contains(bare, "onload") || strings.Contains(bare, "<script>") {
			t.Errorf("theme=%q density=%q: attribute injection reached the output:\n%s", tt.theme, tt.density, out)
		}
	}
}

func TestLayoutAmbientAttribute(t *testing.T) {
	page := NewPage("T", "", "")
	if out := renderWith(t, Layout(page), templ.NopComponent); !strings.Contains(out, `data-ambient="on"`) {
		t.Error("the Ambient light must be on by default")
	}
	page.AmbientOff = true
	if out := renderWith(t, Layout(page), templ.NopComponent); !strings.Contains(out, `data-ambient="off"`) {
		t.Error("AmbientOff must render data-ambient=\"off\"")
	}
}

func TestLayoutStylesheetsAreServedHashedAssets(t *testing.T) {
	out := renderWith(t, Layout(NewPage("T", "", "")), templ.NopComponent)
	hrefs := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)"`).FindAllStringSubmatch(out, -1)
	if len(hrefs) < 2 {
		t.Fatalf("expected tokens.css and base.css links, got %v in\n%s", hrefs, out)
	}
	for _, m := range hrefs {
		req := httptest.NewRequest("GET", m[1], nil)
		rec := httptest.NewRecorder()
		assets.Default.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
			t.Errorf("%s: status %d, type %q", m[1], rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if !strings.Contains(out, "/css/tokens.") || !strings.Contains(out, "/css/base.") {
		t.Errorf("layout should link tokens.css and base.css:\n%s", out)
	}
}

func TestNewPageFallbacks(t *testing.T) {
	p := NewPage("x", "nope", "nope")
	if p.Theme != DefaultTheme || p.Density != DefaultDensity {
		t.Errorf("got %+v", p)
	}
}

// A signed-in page marks <html> so the pre-hydration script stores its
// preferences; an anonymous one does not, so the script applies the stored
// ones instead.
func TestLayoutMarksSignedInPreferences(t *testing.T) {
	anon := renderWith(t, Layout(NewPage("T", "", "")), templ.NopComponent)
	if strings.Contains(anon, "data-prefs=") {
		t.Error("anonymous page carries data-prefs on <html>")
	}
	page := NewPage("T", "", "")
	page.Login = "admin"
	if out := renderWith(t, Layout(page), templ.NopComponent); !strings.Contains(out, `data-prefs="user"`) {
		t.Error("signed-in page lacks data-prefs")
	}
}

// docs/22 "Toasts": both live regions are in the page from the start, the
// polite one for notes and the assertive one for errors, and the script
// that fills them is part of the shell's assets.
func TestToastRegionHasLiveRegions(t *testing.T) {
	html := renderString(t, ToastRegion())
	for _, want := range []string{`data-toast-region`, `role="status" aria-live="polite" data-toast-polite`, `role="alert" data-toast-alert`} {
		if !strings.Contains(html, want) {
			t.Errorf("toast region is missing %q:\n%s", want, html)
		}
	}
	found := false
	for _, s := range ShellScripts {
		found = found || s == "js/toast.js"
	}
	if !found {
		t.Error("js/toast.js is not among the shell's scripts")
	}
}

// Both documents name the icon, so browsers never ask for /favicon.ico.
func TestLayoutsLinkTheIcon(t *testing.T) {
	want := `<link rel="icon" type="image/svg+xml" href="` + assets.URL("img/favicon.svg") + `">`
	for name, out := range map[string]string{
		"app":    renderWith(t, Layout(NewPage("Overview", "", "")), templ.Raw("")),
		"public": renderWith(t, PublicLayout(NewPage("Status", "", ""), "", false, false), templ.Raw("")),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s layout has no %s", name, want)
		}
	}
}
