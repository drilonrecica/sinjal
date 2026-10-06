package web

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
)

const healthDBTimeout = 2 * time.Second

// Health serves /healthz and /readyz (docs/21_SYSTEM_DIAGNOSTICS.md). Both
// are unauthenticated, so responses never carry internal details.
type Health struct {
	reader *sql.DB
	log    *slog.Logger
	ready  atomic.Bool
}

// NewHealth returns a Health that probes the database through reader. It
// starts not ready; call SetReady once startup and migrations are complete.
func NewHealth(reader *sql.DB, logger *slog.Logger) *Health {
	return &Health{reader: reader, log: logging.Sub(logger, "http")}
}

// SetReady marks startup (including migrations) as complete.
func (h *Health) SetReady() { h.ready.Store(true) }

// RegisterHealth mounts the health endpoints.
func RegisterHealth(r chi.Router, h *Health) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/healthz", http.HandlerFunc(h.healthz))
		r.Method(method, "/readyz", http.HandlerFunc(h.readyz))
	}
}

// healthz: the process answers and the database serves a trivial query. It
// uses the read pool so a long write transaction cannot make it time out.
func (h *Health) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthDBTimeout)
	defer cancel()

	var one int
	if err := h.reader.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		h.log.Error("health check failed", "error", err)
		plain(w, r, http.StatusServiceUnavailable, "unhealthy")
		return
	}
	plain(w, r, http.StatusOK, "ok")
}

// readyz: startup and migrations are complete.
func (h *Health) readyz(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		plain(w, r, http.StatusServiceUnavailable, "not ready")
		return
	}
	plain(w, r, http.StatusOK, "ready")
}

func plain(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		io.WriteString(w, body+"\n")
	}
}
