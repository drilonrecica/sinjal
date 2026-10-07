package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The admin API (docs/14_API.md): a small JSON surface for automation,
// under /api/v1. It uses the browser session, like the app itself, and
// protects state changes without the form token: a request must carry
// APIHeader (a custom header cannot be sent cross-site without a CORS
// preflight, which Sinjal never grants) and pass the same Origin check as
// every other state change. Failures are always the JSON error object of
// docs/14, never a redirect or an HTML page.

// APIHeader marks a state-changing API request as made on purpose.
const (
	APIHeader      = "X-Sinjal-Request"
	apiHeaderValue = "1"
)

// API serves /api/v1/status and /api/v1/monitors. It reuses the monitor
// handler's engine, audit trail and clock, so a pause here is the same
// action as the pause button.
type API struct {
	m *Monitors
}

// NewAPI returns the admin API on top of the monitor handler.
func NewAPI(m *Monitors) *API { return &API{m: m} }

type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAPI(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPI(w, status, apiError{apiErrorBody{Code: code, Message: message}})
}

// RegisterAPI mounts the admin API in a group that has LoadSession and
// nothing else: sign-in is required (401), reads are open to viewers,
// state changes need an admin and the API header.
func RegisterAPI(r chi.Router, h *API) {
	r.Use(apiAuth)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/api/v1/status", http.HandlerFunc(h.status))
		r.Method(method, "/api/v1/monitors", http.HandlerFunc(h.list))
		r.Method(method, "/api/v1/monitors/{id}", http.HandlerFunc(h.get))
	}
	r.Group(func(r chi.Router) {
		r.Use(apiGuard, apiAdmin)
		r.Post("/api/v1/monitors/{id}/pause", h.pause)
		r.Post("/api/v1/monitors/{id}/resume", h.resume)
	})
}

func apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := SessionFromContext(r.Context()); !ok {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to use the API")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func apiAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "Your account can view Sinjal but not change it")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiGuard is the API's CSRF protection for state changes: the Origin
// check, then the custom header.
func apiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := checkOrigin(r); reason != "" {
			writeAPIError(w, http.StatusForbidden, "cross_origin", "Cross-origin request refused")
			return
		}
		if r.Header.Get(APIHeader) != apiHeaderValue {
			writeAPIError(w, http.StatusForbidden, "missing_header", "State changes need the "+APIHeader+": "+apiHeaderValue+" header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *API) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.m.log.Error("api request failed", "route", r.URL.Path, "while", what, "error", err)
	writeAPIError(w, http.StatusInternalServerError, "internal", "Something went wrong")
}

type apiMonitor struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	State         string     `json:"state"`
	Enabled       bool       `json:"enabled"`
	StateSince    time.Time  `json:"state_since"`
	IntervalSecs  int        `json:"interval_seconds"`
	LastCheckAt   *time.Time `json:"last_check_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	TLSNotAfter   *time.Time `json:"tls_not_after"`
}

// apiMonitorOf holds what the monitor list shows, never its target, which
// may carry secrets, nor its configuration or failure details.
func apiMonitorOf(m store.Monitor) apiMonitor {
	return apiMonitor{ID: m.ID, Name: m.Name, Type: m.Type, State: displayState(m), Enabled: m.Enabled,
		StateSince: m.StateSince.UTC(), IntervalSecs: m.IntervalSeconds,
		LastCheckAt: utcPtr(m.LastCheckAt), LastSuccessAt: utcPtr(m.LastSuccessAt), LastFailureAt: utcPtr(m.LastFailureAt),
		TLSNotAfter: utcPtr(m.TLSNotAfter)}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (h *API) list(w http.ResponseWriter, r *http.Request) {
	ms, err := store.ListMonitors(r.Context(), h.m.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	out := make([]apiMonitor, 0, len(ms))
	for _, m := range ms {
		out = append(out, apiMonitorOf(m))
	}
	writeAPI(w, http.StatusOK, map[string]any{"monitors": out})
}

func (h *API) get(w http.ResponseWriter, r *http.Request) {
	m, ok := h.monitor(w, r)
	if ok {
		writeAPI(w, http.StatusOK, apiMonitorOf(m))
	}
}

// monitor loads the monitor named in the path, or answers 404 or 500.
func (h *API) monitor(w http.ResponseWriter, r *http.Request) (store.Monitor, bool) {
	m, err := store.GetMonitor(r.Context(), h.m.db.Reader, chi.URLParam(r, "id"))
	switch {
	case err == nil:
		return m, true
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "monitor_not_found", "Monitor not found")
	default:
		h.fail(w, r, "loading a monitor", err)
	}
	return m, false
}

// status summarizes the instance: monitors by displayed state, and the
// ones that need attention.
func (h *API) status(w http.ResponseWriter, r *http.Request) {
	ms, err := store.ListMonitors(r.Context(), h.m.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	counts := map[string]int{templates.StateUp: 0, templates.StateDown: 0, templates.StateFlapping: 0,
		templates.StatePending: 0, templates.StatePaused: 0}
	problems := []apiMonitor{}
	for _, m := range ms {
		s := displayState(m)
		counts[s]++
		if s == templates.StateDown || s == templates.StateFlapping {
			problems = append(problems, apiMonitorOf(m))
		}
	}
	overall := "ok"
	if counts[templates.StateDown] > 0 {
		overall = "down"
	} else if counts[templates.StateFlapping] > 0 {
		overall = "degraded"
	}
	writeAPI(w, http.StatusOK, map[string]any{"status": overall, "generated_at": h.m.now().UTC(),
		"monitors": len(ms), "counts": counts, "problems": problems})
}

func (h *API) pause(w http.ResponseWriter, r *http.Request)  { h.setPaused(w, r, true) }
func (h *API) resume(w http.ResponseWriter, r *http.Request) { h.setPaused(w, r, false) }

// setPaused pauses or resumes a monitor; doing it twice is not an error.
func (h *API) setPaused(w http.ResponseWriter, r *http.Request, pause bool) {
	m, ok := h.monitor(w, r)
	if !ok {
		return
	}
	var changed bool
	var err error
	if pause {
		changed, err = h.m.engine.Pause(r.Context(), m.ID)
	} else {
		changed, err = h.m.engine.Resume(r.Context(), m.ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "monitor_not_found", "Monitor not found")
		return
	}
	if err != nil {
		h.fail(w, r, fmt.Sprintf("changing a monitor (pause=%v)", pause), err)
		return
	}
	if changed {
		typ := audit.MonitorResumed
		if pause {
			typ = audit.MonitorPaused
		}
		cs, _ := SessionFromContext(r.Context())
		h.m.log.Info("monitor "+strings.TrimPrefix(typ, "monitor."), "user_id", cs.User.ID, "monitor_id", m.ID, "via", "api")
		h.m.audit(r, typ, m.ID, m.Name)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
