package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Monitors serves the monitor fragments the live pages refresh
// (docs/33_FRONTEND_COMPONENTS.md). Fragments are plain HTML with no layout;
// htmx swaps them in when an SSE event names their monitor.
type Monitors struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time
}

// NewMonitors returns the monitor fragment handler.
func NewMonitors(d *db.DB, logger *slog.Logger) *Monitors {
	return &Monitors{db: d, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterMonitors mounts the live monitor fragments inside RequireAuth.
// They are read-only and viewers see them too, minus the checked address.
func RegisterMonitors(r chi.Router, h *Monitors) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/fragments/monitors/{id}/row", h.fragment(func(m templates.MonitorView) templ.Component { return templates.MonitorRow(m) }))
		r.Method(method, "/fragments/monitors/{id}/header", h.fragment(func(m templates.MonitorView) templ.Component { return templates.MonitorHeader(m) }))
	}
}

func isAdmin(r *http.Request) bool {
	cs, ok := SessionFromContext(r.Context())
	return ok && cs.User.Role == "admin"
}

// viewInput is what a MonitorView needs besides the monitor row.
type viewInput struct {
	Tags       []string
	Latency    time.Duration
	HasLatency bool
	URL        string // checked address, only read for admins
	Parent     string // parent monitor's name
	Admin      bool
}

// monitorView turns a monitor row into what the templates show.
func monitorView(m store.Monitor, in viewInput, now time.Time) templates.MonitorView {
	v := templates.MonitorView{
		ID:        m.ID,
		Name:      m.Name,
		Type:      m.Type,
		State:     displayState(m),
		Since:     formatSince(now.Sub(m.StateSince)),
		SinceAt:   m.StateSince.UTC().Format(time.RFC3339),
		Latency:   "—",
		LastCheck: "never",
		Uptime:    "—",
		Parent:    in.Parent,
		Tags:      in.Tags,
	}
	if in.HasLatency {
		v.Latency = formatLatency(in.Latency)
	}
	if m.LastCheckAt != nil {
		v.LastCheck = formatSince(now.Sub(*m.LastCheckAt)) + " ago"
	}
	if in.Admin {
		v.Target = targetSummary(in.URL)
	}
	return v
}

// displayState folds enabled and the flapping overlay into one state.
func displayState(m store.Monitor) string {
	switch {
	case !m.Enabled || m.State == "paused":
		return templates.StatePaused
	case m.FlappingSince != nil:
		return templates.StateFlapping
	}
	return m.State
}

// targetSummary is scheme://host of a checked address: no credentials, path
// or query, which may carry secrets (docs/13_AUTH_SECURITY.md).
func targetSummary(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func formatLatency(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1 ms"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// formatSince renders a duration at two units at most: "42s", "5m", "2h 5m",
// "3d 4h". A negative duration (clock skew) reads as "0s".
func formatSince(d time.Duration) string {
	d = d.Truncate(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return joinUnits(int(d.Hours()), "h", int(d.Minutes())%60, "m")
	}
	return joinUnits(int(d.Hours())/24, "d", int(d.Hours())%24, "h")
}

func joinUnits(a int, au string, b int, bu string) string {
	if b == 0 {
		return fmt.Sprintf("%d%s", a, au)
	}
	return fmt.Sprintf("%d%s %d%s", a, au, b, bu)
}

// view loads one monitor for display, or store.ErrNotFound.
func (h *Monitors) view(r *http.Request, id string) (templates.MonitorView, error) {
	ctx, q := r.Context(), h.db.Reader
	m, err := store.GetMonitor(ctx, q, id)
	if err != nil {
		return templates.MonitorView{}, err
	}
	in := viewInput{Admin: isAdmin(r)}
	if in.Tags, err = store.MonitorTags(ctx, q, id); err != nil {
		return templates.MonitorView{}, err
	}
	if in.Latency, in.HasLatency, err = store.LastDuration(ctx, q, id); err != nil {
		return templates.MonitorView{}, err
	}
	if in.Admin && m.Type == "http" {
		cfg, err := store.GetHTTPConfig(ctx, q, id)
		if err != nil {
			return templates.MonitorView{}, err
		}
		in.URL = cfg.URL
	}
	if m.ParentMonitorID != "" {
		p, err := store.GetMonitor(ctx, q, m.ParentMonitorID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return templates.MonitorView{}, err
		}
		in.Parent = p.Name
	}
	return monitorView(m, in, h.now()), nil
}

// fragment serves one component for the monitor named in the URL.
func (h *Monitors) fragment(component func(templates.MonitorView) templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h.view(r, chi.URLParam(r, "id"))
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		case err != nil:
			h.log.Error("monitor fragment failed", "route", r.URL.Path, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		default:
			render(w, r, h.log, http.StatusOK, component(v))
		}
	}
}
