package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/web/middleware"
)

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func router(buf *bytes.Buffer) *chi.Mux {
	log := newLogger(buf)
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.AccessLog(log), middleware.Recover(log))
	return r
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequestIDUniqueAndInboundIgnored(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	var seen string
	r.Get("/x", func(w http.ResponseWriter, req *http.Request) {
		seen = middleware.GetRequestID(req.Context())
	})

	a := do(r, "GET", "/x", map[string]string{"X-Request-Id": "forged\nlevel=ERROR"})
	b := do(r, "GET", "/x", nil)

	idA, idB := a.Header().Get("X-Request-Id"), b.Header().Get("X-Request-Id")
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(idA) {
		t.Errorf("request id = %q, want 16 hex chars", idA)
	}
	if idA == idB {
		t.Error("request IDs are not unique")
	}
	if strings.Contains(buf.String(), "forged") {
		t.Errorf("inbound X-Request-Id leaked into the logs: %s", buf.String())
	}
	if seen != idB {
		t.Errorf("context id %q != header id %q", seen, idB)
	}
}

func TestAccessLogFields(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("short and stout"))
	})

	rec := do(r, "GET", "/hello", nil)
	out := buf.String()
	for _, want := range []string{
		"msg=request", "method=GET", "route=/hello", "status=418", "bytes=15",
		"request_id=" + rec.Header().Get("X-Request-Id"), "duration_ms=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("access log %q is missing %q", out, want)
		}
	}
}

func TestAccessLogDefaultStatus(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("hi")) })
	do(r, "GET", "/ok", nil)
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("log %q should record an implicit 200", buf.String())
	}
}

func TestAccessLogNeverContainsPathTokensOrQuery(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/s/{token}", func(w http.ResponseWriter, _ *http.Request) {})

	do(r, "GET", "/s/SECRET-TOKEN?key=SECRET-QUERY", nil)
	do(r, "GET", "/nowhere/SECRET-OTHER", nil) // unmatched

	out := buf.String()
	for _, secret := range []string{"SECRET-TOKEN", "SECRET-QUERY", "SECRET-OTHER"} {
		if strings.Contains(out, secret) {
			t.Errorf("access log leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "route=/s/{token}") {
		t.Errorf("expected the route pattern in %q", out)
	}
	if !strings.Contains(out, "route=-") || !strings.Contains(out, "status=404") {
		t.Errorf("unmatched request should log route=- status=404: %q", out)
	}
}

func TestUnmatchedRouteStillGetsMiddleware(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/only", func(w http.ResponseWriter, _ *http.Request) {})
	rec := do(r, "GET", "/missing", nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Request-Id") == "" {
		t.Errorf("status = %d, request id = %q", rec.Code, rec.Header().Get("X-Request-Id"))
	}
}

func TestRecoverReturns500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	r.Get("/fine", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	rec := do(r, "GET", "/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "kaboom") || strings.Contains(body, "goroutine") {
		t.Errorf("response body leaks panic details: %q", body)
	}
	out := buf.String()
	for _, want := range []string{"level=ERROR", "panic recovered", "kaboom", "stack=", "route=/boom", "status=500",
		"request_id=" + rec.Header().Get("X-Request-Id")} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}
	if rec := do(r, "GET", "/fine", nil); rec.Code != http.StatusOK {
		t.Errorf("router stopped serving after a panic: %d", rec.Code)
	}
}

func TestRecoverAfterResponseStarted(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/late", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	})
	rec := do(r, "GET", "/late", nil)
	if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("status = %d body = %q; the started response must be left alone", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Error("panic was not logged")
	}
}

func TestRecoverRepanicsAbortHandler(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	r.Get("/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
		if strings.Contains(buf.String(), "panic recovered") {
			t.Error("ErrAbortHandler must not be logged as a crash")
		}
	}()
	do(r, "GET", "/abort", nil)
}

func TestWriterUnwrapKeepsFlush(t *testing.T) {
	var buf bytes.Buffer
	r := router(&buf)
	var flushErr error
	r.Get("/stream", func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	})
	do(r, "GET", "/stream", nil)
	if flushErr != nil {
		t.Errorf("ResponseController.Flush through the middleware: %v", flushErr)
	}
}
