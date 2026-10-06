package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/sinjal/internal/db"
)

func healthFixture(t *testing.T) (*Health, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	logger, _ := quietLogger()
	h := NewHealth(d.Reader, logger)
	r := NewRouter(logger, nil)
	RegisterHealth(r, h)
	return h, r
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthzOK(t *testing.T) {
	_, r := healthFixture(t)
	rec := get(r, "GET", "/healthz")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Errorf("GET /healthz = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("middleware did not run: no X-Request-Id")
	}
}

func TestHealthzFailsWhenDatabaseUnavailable(t *testing.T) {
	logger, logs := quietLogger()
	d, err := db.Open(filepath.Join(t.TempDir(), "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHealth(d.Reader, logger)
	r := NewRouter(logger, nil)
	RegisterHealth(r, h)
	d.Close()

	rec := get(r, "GET", "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "unhealthy" {
		t.Errorf("body = %q; it must not carry error details", body)
	}
	if !strings.Contains(logs.String(), "health check failed") {
		t.Errorf("failure was not logged: %s", logs.String())
	}
}

func TestReadyzFollowsFlag(t *testing.T) {
	h, r := healthFixture(t)

	rec := get(r, "GET", "/readyz")
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != "not ready" {
		t.Errorf("before SetReady: %d %q", rec.Code, rec.Body.String())
	}
	h.SetReady()
	rec = get(r, "GET", "/readyz")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "ready" {
		t.Errorf("after SetReady: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("readyz must not be cacheable")
	}
}

func TestHealthHEAD(t *testing.T) {
	_, r := healthFixture(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := get(r, "HEAD", path)
		if rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotFound {
			t.Errorf("HEAD %s = %d", path, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD %s returned a body", path)
		}
	}
}

func TestHealthRejectsWrites(t *testing.T) {
	_, r := healthFixture(t)
	if rec := get(r, "POST", "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}
