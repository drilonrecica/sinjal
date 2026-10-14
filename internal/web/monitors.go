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
	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/internal/web/sse"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Monitors serves the monitor pages, their forms and the fragments the
// live pages refresh (docs/33_FRONTEND_COMPONENTS.md). Fragments are plain
// HTML with no layout; htmx swaps them in when an SSE event names their
// monitor. Changes go to the store, then to the engine's schedule, then out
// as events.
type Monitors struct {
	db     *db.DB
	key    *vault.Key
	engine *engine.Engine
	events *sse.Hub
	log    *slog.Logger
	loc    *time.Location // the instance time zone, for history ranges
	now    func() time.Time
}

// NewMonitors returns the monitor handler; loc nil means UTC.
func NewMonitors(d *db.DB, key *vault.Key, eng *engine.Engine, events *sse.Hub, loc *time.Location, logger *slog.Logger) *Monitors {
	if loc == nil {
		loc = time.UTC
	}
	return &Monitors{db: d, key: key, engine: eng, events: events, loc: loc, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterMonitors mounts the monitor list, the detail page and their live
// fragments inside RequireAuth. They are read-only and viewers see them
// too, minus the checked address and failure details.
func RegisterMonitors(r chi.Router, h *Monitors) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/monitors/{id}", http.HandlerFunc(h.detail))
		r.Method(method, "/fragments/monitors", http.HandlerFunc(h.column))
		r.Method(method, "/fragments/activity", http.HandlerFunc(h.activity))
		r.Method(method, "/fragments/palette", http.HandlerFunc(h.palette))
		r.Method(method, "/fragments/monitors/{id}/row", h.fragment(rowBar, func(m templates.MonitorView) templ.Component { return templates.ColumnRow(m) }))
		r.Method(method, "/fragments/monitors/{id}/beats", http.HandlerFunc(h.beatTips))
		r.Method(method, "/fragments/monitors/{id}/header", h.fragment(heroBar, func(m templates.MonitorView) templ.Component { return templates.MonitorHeader(m) }))
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
	Target     string // store.Target, only read for admins
	Parent     string // parent monitor's name
	ParentDown bool   // the parent monitor is down
	Uptime     string // 24 hours, raw; the detail header only ("" reads as "—")
	Adjusted   string // the same with maintenance excluded
	Admin      bool
}

// monitorView turns a monitor row into what the templates show.
func monitorView(m store.Monitor, in viewInput, now time.Time) templates.MonitorView {
	v := templates.MonitorView{
		ID:         m.ID,
		Name:       m.Name,
		Type:       m.Type,
		State:      displayState(m),
		Since:      formatSince(now.Sub(m.StateSince)),
		SinceAt:    m.StateSince.UTC().Format(time.RFC3339),
		Latency:    "—",
		LastCheck:  "never",
		Uptime:     "—",
		Parent:     in.Parent,
		Tags:       in.Tags,
		ParentDown: in.ParentDown,
	}
	if in.Uptime != "" {
		v.Uptime = in.Uptime
		if in.Adjusted != in.Uptime {
			v.UptimeAdjusted = in.Adjusted
		}
	}
	if in.HasLatency {
		v.Latency = formatLatency(in.Latency)
	}
	if m.LastCheckAt != nil {
		v.LastCheck = formatSince(now.Sub(*m.LastCheckAt)) + " ago"
	}
	if in.Admin {
		v.Target = targetSummary(m.Type, in.Target)
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

// targetSummary is what the list shows of a monitor's target (raw is
// store.Target): scheme://host of an HTTP address, without credentials,
// path or query, which may carry secrets (docs/13_AUTH_SECURITY.md);
// host:port, host or "hostname TYPE" as stored; a heartbeat monitor's
// source label, or "push" without one. Never a heartbeat token.
func targetSummary(typ, raw string) string {
	switch typ {
	case store.TypeHTTP:
	case store.TypeHeartbeat:
		if raw == "" {
			return "push"
		}
		return raw
	default:
		return raw
	}
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

// column serves GET /fragments/monitors: the shell's monitor list column,
// loaded once per page load and again when a monitor is created or
// deleted (docs/03 "Layout").
func (h *Monitors) column(w http.ResponseWriter, r *http.Request) {
	admin := isAdmin(r)
	rows, err := h.rows(r, admin)
	if err != nil {
		h.log.Error("monitor column failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.ColumnList(rows, admin))
}

// rows builds every row of the column in a fixed number of queries,
// however many monitors there are: the monitors, their tags, for admins
// their targets (what the column's search matches), and the 24-hour raw
// uptime of all of them at once. The beat bars come from the engine's
// memory.
func (h *Monitors) rows(r *http.Request, admin bool) ([]templates.MonitorView, error) {
	ctx, q := r.Context(), h.db.Reader
	now := h.now()
	monitors, err := store.ListMonitors(ctx, q)
	var tags map[string][]string
	var targets map[string]string
	var uptimes map[string]incident.Ratio
	if err == nil {
		tags, err = store.TagsByMonitor(ctx, q)
	}
	if err == nil && admin {
		targets, err = store.Targets(ctx, q)
	}
	if err == nil {
		uptimes, err = store.UptimesRaw(ctx, q, now.Add(-24*time.Hour), now, now)
	}
	if err != nil {
		return nil, err
	}
	out := make([]templates.MonitorView, 0, len(monitors))
	buf := make([]recent.Check, 0, listBeats)
	clock := newBeatClock(now, h.loc)
	for _, m := range monitors {
		u := uptimes[m.ID]
		v := monitorView(m, viewInput{Tags: tags[m.ID], Target: targets[m.ID], Uptime: u.Percent(), Adjusted: u.Percent(), Admin: admin}, now)
		buf = h.engine.Recent().Newest(m.ID, rowBar.n, buf[:0])
		v.Beats = beatBar(m.ID, buf, rowBar, clock)
		out = append(out, v)
	}
	return out, nil
}

// view loads one monitor for display, with a beat bar of kind bar, or
// store.ErrNotFound. It returns the row as well, for pages that show more
// than the view.
func (h *Monitors) view(r *http.Request, id string, bar barKind) (store.Monitor, templates.MonitorView, error) {
	ctx, q := r.Context(), h.db.Reader
	m, err := store.GetMonitor(ctx, q, id)
	if err != nil {
		return m, templates.MonitorView{}, err
	}
	in := viewInput{Admin: isAdmin(r)}
	if in.Tags, err = store.MonitorTags(ctx, q, id); err != nil {
		return m, templates.MonitorView{}, err
	}
	if in.Latency, in.HasLatency, err = store.LastDuration(ctx, q, id); err != nil {
		return m, templates.MonitorView{}, err
	}
	if in.Admin {
		if in.Target, err = store.Target(ctx, q, id); err != nil {
			return m, templates.MonitorView{}, err
		}
	}
	// The list does not show uptime: reading it costs about 140 µs per
	// monitor, 140 ms for 1,000, over the list's budget (docs/18).
	now := h.now()
	raw, adjusted, err := store.Uptime(ctx, q, id, now.Add(-24*time.Hour), now, now, h.loc)
	if err != nil {
		return m, templates.MonitorView{}, err
	}
	in.Uptime, in.Adjusted = raw.Percent(), adjusted.Percent()
	if m.ParentMonitorID != "" {
		p, err := store.GetMonitor(ctx, q, m.ParentMonitorID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return m, templates.MonitorView{}, err
		}
		in.Parent, in.ParentDown = p.Name, p.State == "down"
	}
	v := monitorView(m, in, now)
	v.Beats = beatBar(id, h.engine.Recent().Newest(id, bar.n, nil), bar, newBeatClock(now, h.loc))
	return m, v, nil
}

// fragment serves one component for the monitor named in the URL.
func (h *Monitors) fragment(bar barKind, component func(templates.MonitorView) templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, v, err := h.view(r, chi.URLParam(r, "id"), bar)
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
