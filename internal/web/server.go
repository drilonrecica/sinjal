package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/drilonrecica/sinjal/internal/logging"
)

// ShutdownGrace bounds how long a graceful shutdown waits for in-flight
// requests before connections are closed (docs/07_SCHEDULER.md).
const ShutdownGrace = 10 * time.Second

// NewServer returns an http.Server with explicit timeouts. WriteTimeout also
// applies to streaming responses; SSE handlers must extend their own deadline
// with http.ResponseController.SetWriteDeadline.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Run serves on ln until ctx is cancelled, then shuts down gracefully:
// it stops accepting connections and waits up to grace for in-flight
// requests, after which remaining connections are closed. It returns an
// error only if serving itself failed.
//
// The server's base context is deliberately not tied to ctx, so in-flight
// requests are drained rather than cancelled the moment shutdown starts.
func Run(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration, logger *slog.Logger) error {
	log := logging.Sub(logger, "http")

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "grace", grace.String())
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		// Bounded by design: do not hang on a stuck client or handler.
		log.Warn("graceful shutdown timed out; closing remaining connections", "error", err)
		_ = srv.Close()
	}
	<-served
	return nil
}
