package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

// statusWriter records the status code and body size. It exposes the wrapped
// writer through Unwrap, so handlers keep Flush/deadline control via
// http.ResponseController (needed for SSE).
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// AccessLog logs one line per request after it completes.
//
// It logs the matched route pattern (for example /s/{token}), never the raw
// path or query string: unlisted status-page tokens and heartbeat tokens live
// in URLs and must not reach the logs. Requests that match no route log "-".
// client_ip is the address resolved under the trusted-proxy rules.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("request",
				"request_id", GetRequestID(r.Context()),
				"client_ip", proxy.ClientIP(r).String(),
				"method", r.Method,
				"route", routePattern(r),
				"status", sw.status,
				"bytes", sw.bytes,
				"duration_ms", float64(time.Since(start).Microseconds())/1000,
			)
		})
	}
}

func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "-"
}
