package web

import (
	"net/http"
	"testing"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
)

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newAppEnv(t)
	logger, _ := quietLogger()
	panicky := NewRouter(logger, nil)
	panicky.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	cases := []struct {
		name string
		h    http.Handler
		path string
		code int
	}{
		{"page", e.h, "/login", 200},
		{"redirect to login", e.h, "/monitors", 303},
		{"not found", e.h, "/nope", 404},
		{"static asset", e.h, assets.URL("css/base.css"), 200},
		{"health", e.h, "/healthz", 200},
		{"closed setup", e.h, "/setup", 404},
		{"panic", panicky, "/boom", 500},
	}
	want := map[string]string{
		"Content-Security-Policy":    middleware.ContentSecurityPolicy,
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         middleware.PermissionsPolicy,
	}
	for _, c := range cases {
		rec := get(c.h, "GET", c.path)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.code)
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got, v)
			}
		}
		// /setup keeps its stricter override (the token is in its URL).
		wantRef := "same-origin"
		if c.path == "/setup" {
			wantRef = "no-referrer"
		}
		if got := rec.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != wantRef {
			t.Errorf("%s: Referrer-Policy = %q, want %q", c.name, got, wantRef)
		}
	}
}
