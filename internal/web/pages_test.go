package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/web/templates"
)

func pagesRouter() (http.Handler, *lockedBuffer) {
	logger, logs := quietLogger()
	r := NewRouter(logger, nil)
	RegisterPages(r, logger)
	RegisterStatic(r, assets.Default)
	return r, logs
}

var navLinkRe = regexp.MustCompile(`<a class="nav-link" href="([^"]+)"( aria-current="page")?>([^<]+)</a>`)

func TestEverySectionRendersTheShell(t *testing.T) {
	r, _ := pagesRouter()
	for _, s := range templates.Sections {
		t.Run(s.Key, func(t *testing.T) {
			rec := get(r, "GET", s.Path)
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
			}
			body := rec.Body.String()

			// Landmarks and heading.
			for _, want := range []string{
				`<main id="main" class="content" tabindex="-1">`,
				`<nav class="nav" aria-label="Primary">`,
				`<header class="brand">`,
				"<h1>" + s.Label + "</h1>",
				"<title>" + s.Label + " — Sinjal</title>",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}

			// Navigation: seven links in IA order, exactly one current.
			links := navLinkRe.FindAllStringSubmatch(body, -1)
			if len(links) != len(templates.Sections) {
				t.Fatalf("found %d nav links, want %d", len(links), len(templates.Sections))
			}
			current := 0
			for i, l := range links {
				want := templates.Sections[i]
				if l[1] != want.Path || l[3] != want.Label {
					t.Errorf("nav link %d = %s %q, want %s %q", i, l[1], l[3], want.Path, want.Label)
				}
				if l[2] != "" {
					current++
					if want.Key != s.Key {
						t.Errorf("aria-current is on %q, want %q", want.Key, s.Key)
					}
				}
			}
			if current != 1 || strings.Count(body, `aria-current="page"`) != 1 {
				t.Errorf("want exactly one aria-current=page, got %d", strings.Count(body, `aria-current="page"`))
			}
		})
	}
}

func TestSkipLinkComesFirstAndTargetsMain(t *testing.T) {
	r, _ := pagesRouter()
	body := get(r, "GET", "/").Body.String()

	firstLink := strings.Index(body, "<a ")
	skip := strings.Index(body, `<a class="skip-link" href="#main">`)
	nav := strings.Index(body, "<nav")
	if skip < 0 || skip != firstLink {
		t.Errorf("skip link must be the first link in the document (skip=%d first=%d)", skip, firstLink)
	}
	if nav < skip {
		t.Error("navigation precedes the skip link")
	}
	if strings.Count(body, `id="main"`) != 1 {
		t.Error("expected exactly one #main target")
	}
}

func TestShellLoadsHashedAssets(t *testing.T) {
	r, _ := pagesRouter()
	body := get(r, "GET", "/").Body.String()

	hrefs := regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`).FindAllStringSubmatch(body, -1)
	var css, js int
	for _, m := range hrefs {
		rec := get(r, "GET", m[1])
		if rec.Code != 200 {
			t.Errorf("%s = %d", m[1], rec.Code)
		}
		switch {
		case strings.HasSuffix(m[1], ".css"):
			css++
		case strings.HasSuffix(m[1], ".js"):
			js++
		}
	}
	if css != 3 || js != 1 {
		t.Errorf("assets linked: %d css, %d js; want 3 (tokens, base, shell) and 1 (htmx)", css, js)
	}
	if !regexp.MustCompile(`<script src="/static/js/htmx\.min\.[0-9a-f]{16}\.js" defer>`).MatchString(body) {
		t.Error("htmx must load with defer from its hashed URL")
	}
	if !strings.Contains(body, `data-theme="carbon"`) || !strings.Contains(body, `data-density="comfortable"`) {
		t.Error("html element must carry the default theme and density")
	}
}

func TestPagesHEAD(t *testing.T) {
	r, _ := pagesRouter()
	rec := get(r, "HEAD", "/monitors")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD /monitors = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestPagesRejectWritesAndUnknownPaths(t *testing.T) {
	r, _ := pagesRouter()
	if rec := get(r, "GET", "/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", rec.Code)
	}
	if rec := get(r, "POST", "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

func TestRenderFailureIsACleanInternalError(t *testing.T) {
	logger, logs := quietLogger()
	failing := templ.ComponentFunc(func(ctx_ context.Context, w io.Writer) error {
		io.WriteString(w, "<html>half a page")
		return errors.New("boom: secret detail")
	})
	rec := httptest.NewRecorder()
	render(rec, httptest.NewRequest("GET", "/x", nil), logger, 200, failing)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "half a page") || strings.Contains(body, "secret detail") {
		t.Errorf("response leaked partial output or error text: %q", body)
	}
	if !strings.Contains(logs.String(), "render failed") || !strings.Contains(logs.String(), "secret detail") {
		t.Errorf("failure was not logged with detail: %s", logs.String())
	}
}
