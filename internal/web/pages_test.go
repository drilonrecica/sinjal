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
		if s.Key == "maintenance" || s.Key == "incidents" || s.Key == "notifications" || s.Key == "status-pages" {
			continue // real pages needing the database: see maintenance_test.go, incidents_test.go, notifications_test.go, statuspages_test.go
		}
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
				`<main id="main" class="content" tabindex="-1" data-view="detail" hx-history-elt>`,
				`<nav id="topnav" class="topnav" aria-label="Primary">`,
				`<header class="topbar">`,
				`<aside class="monitor-column" aria-label="Monitors">`,
				`hx-get="/fragments/monitors" hx-trigger="load"`,
				"<h1>" + s.Label + "</h1>",
				"<title>" + s.Label + " — Sinjal</title>",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}

			// Navigation: the sections a non-admin may open, in IA order,
			// exactly one current. (No session here: not an admin.)
			var visible []templates.Section
			for _, s := range templates.Sections {
				if !s.Admin {
					visible = append(visible, s)
				}
			}
			links := navLinkRe.FindAllStringSubmatch(body, -1)
			if len(links) != len(visible) {
				t.Fatalf("found %d nav links, want %d", len(links), len(visible))
			}
			current := 0
			for i, l := range links {
				want := visible[i]
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
	body := get(r, "GET", "/settings/general").Body.String()

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
	body := get(r, "GET", "/settings/general").Body.String()

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
	// The shell links the same assets on every page (it swaps only its
	// pane): the three base stylesheets and the shell's own set.
	if css != 3+len(templates.ShellStyles) || js != len(templates.ShellScripts) {
		t.Errorf("assets linked: %d css, %d js; want %d and %d", css, js, 3+len(templates.ShellStyles), len(templates.ShellScripts))
	}
	if !regexp.MustCompile(`<script src="/static/js/htmx\.min\.[0-9a-f]{16}\.js" defer>`).MatchString(body) {
		t.Error("htmx must load with defer from its hashed URL")
	}
	if !strings.Contains(body, `data-theme="stark"`) || !strings.Contains(body, `data-density="comfortable"`) {
		t.Error("html element must carry the default theme and density")
	}
}

func TestPagesHEAD(t *testing.T) {
	r, _ := pagesRouter()
	rec := get(r, "HEAD", "/settings/general")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD /settings/general = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestPagesRejectWritesAndUnknownPaths(t *testing.T) {
	r, _ := pagesRouter()
	if rec := get(r, "GET", "/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", rec.Code)
	}
	if rec := get(r, "POST", "/settings/general"); rec.Code != http.StatusMethodNotAllowed {
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

// Status Pages are admin-only, so only admins see the link to them; both
// roles get the rest of the top bar and the account menu with their name.
func TestTopBarByRole(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	admin := e.getAs(t, "a1", "GET", "/incidents").Body.String()
	viewer := e.getAs(t, "v1", "GET", "/incidents").Body.String()
	link := `<a class="nav-link" href="/status-pages">Status Pages</a>`
	if !strings.Contains(admin, link) || strings.Contains(viewer, link) {
		t.Errorf("Status Pages link: admin %v, viewer %v", strings.Contains(admin, link), strings.Contains(viewer, link))
	}
	for who, body := range map[string]string{"admin": admin, "viewer": viewer} {
		if !strings.Contains(body, `<a class="nav-link" href="/incidents" aria-current="page">Incidents</a>`) ||
			!strings.Contains(body, `<summary class="account-button">`+who+`</summary>`) {
			t.Errorf("%s: top bar lacks the current section or the account menu", who)
		}
	}
}
