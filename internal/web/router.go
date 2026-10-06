// Package web wires the HTTP layer: router, middleware and server lifecycle.
package web

import (
	"log/slog"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

// NewRouter returns the root router with the common middleware installed:
// request ID, trusted-proxy resolution, access log, panic recovery (outermost
// first). trusted is SINJAL_TRUSTED_PROXIES. Callers register routes on the
// result.
//
// chi only builds its middleware chain once the first route is registered, so
// a router with no routes answers 404 without running any middleware.
func NewRouter(logger *slog.Logger, trusted []netip.Prefix) *chi.Mux {
	log := logging.Sub(logger, "http")
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		proxy.Middleware(proxy.New(trusted)),
		middleware.AccessLog(log),
		middleware.Recover(log),
	)
	return r
}
