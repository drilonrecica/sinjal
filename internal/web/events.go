package web

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/sse"
)

// RegisterEvents mounts GET /events, the live-update stream for signed-in
// users (docs/32_SSE_EVENTS.md). It must be mounted behind RequireAuth.
func RegisterEvents(r chi.Router, hub *sse.Hub, sessions *auth.Sessions, logger *slog.Logger) {
	log := logging.Sub(logger, "http")
	r.Get("/events", func(w http.ResponseWriter, req *http.Request) {
		hub.Serve(w, req, sessionAlive(sessions, req, log))
	})
}

// sessionAlive reports whether the session that opened a stream still
// exists. RequireAuth only looks when the stream opens; without this a
// stream would go on after a logout, an expired session or a disabled
// account. Only a definite "no session" ends the stream: a database error
// is logged and asked about again at the next keepalive.
func sessionAlive(sessions *auth.Sessions, r *http.Request, log *slog.Logger) func() bool {
	var token string
	if c, err := r.Cookie(sessionCookieName(r)); err == nil {
		token = c.Value
	}
	return func() bool {
		_, _, err := sessions.Lookup(r.Context(), token, time.Now())
		if err != nil && !errors.Is(err, auth.ErrNoSession) && r.Context().Err() == nil {
			log.Warn("event stream: session check failed", "error", err)
		}
		return !errors.Is(err, auth.ErrNoSession)
	}
}
