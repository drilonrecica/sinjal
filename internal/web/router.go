// Package web wires the HTTP layer: router, middleware and server lifecycle.
package web

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
)

// NewRouter returns the root router with the common middleware installed:
// request ID, access log, panic recovery (outermost first). Callers register
// routes on the result.
//
// chi only builds its middleware chain once the first route is registered, so
// a router with no routes answers 404 without running any middleware.
func NewRouter(logger *slog.Logger) *chi.Mux {
	log := logging.Sub(logger, "http")
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		middleware.AccessLog(log),
		middleware.Recover(log),
	)
	return r
}
