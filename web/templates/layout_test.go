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
		{"", "", "carbon", "comfortable", "dark"},
		{"paper", "compact", "paper", "compact", "light"},
		{"midnight", "comfortable", "midnight", "comfortable", "dark"},
		{"terminal", "compact", "terminal", "compact", "dark"},
		{"neon", "huge", "carbon", "comfortable", "dark"},
		{`paper" onload="alert(1)`, `x"><script>`, "carbon", "comfortable", "dark"},
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
		if strings.Contains(out, "onload") || strings.Contains(out, "<script>") {
			t.Errorf("theme=%q density=%q: attribute injection reached the output:\n%s", tt.theme, tt.density, out)
		}
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
