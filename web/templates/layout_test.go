package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
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
	out := renderWith(t, Layout("Overview"), templ.Raw("<p>hello</p>"))

	for _, want := range []string{
		"<!doctype html>", `<html lang="en">`, `<meta charset="utf-8">`,
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
	out := renderWith(t, Layout(`<script>alert(1)</script>`), templ.NopComponent)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("title was not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected an escaped title:\n%s", out)
	}
}
