package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

// Heartbeat push limits: requests per client address and window, and how
// many addresses are tracked (docs/14: light abuse protection only).
const (
	heartbeatLimit    = 60
	heartbeatWindow   = time.Minute
	heartbeatCapacity = 10000
)

// Heartbeat serves the heartbeat push endpoint (docs/14). It is a machine
// endpoint: the token authenticates, there is no session and no CSRF
// check. The token is in the path or a bearer header, never logged (the
// access log records the route pattern), and the body is not read.
type Heartbeat struct {
	engine  *engine.Engine
	limiter *ratelimit.Limiter
	log     *slog.Logger
	now     func() time.Time
}

// NewHeartbeat returns the endpoint, recording beats through eng.
func NewHeartbeat(eng *engine.Engine, logger *slog.Logger) *Heartbeat {
	return &Heartbeat{
		engine:  eng,
		limiter: ratelimit.New(heartbeatLimit, heartbeatWindow, heartbeatCapacity),
		log:     logging.Sub(logger, "heartbeat"),
		now:     time.Now,
	}
}

// RegisterHeartbeat mounts GET and POST /api/v1/heartbeat/{token}, and
// POST /api/v1/heartbeat with "Authorization: Bearer <token>".
func RegisterHeartbeat(r chi.Router, h *Heartbeat) {
	r.Get("/api/v1/heartbeat/{token}", h.pathToken)
	r.Post("/api/v1/heartbeat/{token}", h.pathToken)
	r.Post("/api/v1/heartbeat", h.bearerToken)
}

func (h *Heartbeat) pathToken(w http.ResponseWriter, r *http.Request) {
	h.beat(w, r, chi.URLParam(r, "token"))
}

func (h *Heartbeat) bearerToken(w http.ResponseWriter, r *http.Request) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		token = ""
	}
	h.beat(w, r, strings.TrimSpace(token))
}

func (h *Heartbeat) beat(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Cache-Control", "no-store")
	// Every request counts, valid or not: guessing tokens costs the same
	// as sending beats too fast.
	ip, now := proxy.ClientIP(r).String(), h.now()
	if h.limiter.Blocked(ip, now) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	h.limiter.Add(ip, now)

	err := h.engine.Beat(r.Context(), token)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, engine.ErrUnknownToken):
		http.Error(w, "unknown heartbeat token", http.StatusNotFound)
	case r.Context().Err() != nil:
		// The client went away; nothing to answer.
	default:
		h.log.Error("heartbeat not recorded", "error", err)
		http.Error(w, "heartbeat not recorded", http.StatusInternalServerError)
	}
}
