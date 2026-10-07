package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
	// stripDays is the length of the uptime strip (docs/12, P0-15).
	stripDays = 90
	// dayDownBelow: a day under this adjusted uptime is drawn as down,
	// one at or above it with some downtime as partial.
	dayDownBelow = 0.95
	// publicIncidentLimit bounds the incidents a page lists.
	publicIncidentLimit = 50
	// publicCacheTTL is how long a page's figures are reused: visitors of
	// a busy page share one read of the database. Every admin change to a
	// page moves its updated_at, which ends the entry at once.
	publicCacheTTL = 10 * time.Second
)

// Public serves the status pages to their visitors (docs/12). It reads
// only what a page shows: public names and figures, never a monitor's own
// name, target, failure text or snippet.
type Public struct {
	db      *db.DB
	key     []byte // signs page cookies (PageKeyLabel)
	limiter *ratelimit.Limiter
	loc     *time.Location
	log     *slog.Logger
	now     func() time.Time
	cache   pageCache
}

// NewPublic returns the public status page handler; key signs the cookies
// of password-protected pages; loc nil means UTC.
func NewPublic(d *db.DB, key []byte, loc *time.Location, logger *slog.Logger) *Public {
	if loc == nil {
		loc = time.UTC
	}
	return &Public{db: d, key: key, limiter: ratelimit.New(pageMaxFailures, pageWindow, pageTrackedKeys), loc: loc,
		log: logging.Sub(logger, "http"), now: time.Now, cache: pageCache{entries: map[string]cachedPage{}}}
}

// RegisterPublic mounts the path-based and unlisted pages. They sit in
// the session group, outside RequireAuth: each page enforces its own
// visibility (pageaccess.go). POST is the page password form.
func RegisterPublic(r chi.Router, h *Public) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/status/{slug}", http.HandlerFunc(h.bySlug))
		r.Method(method, "/s/{token}", http.HandlerFunc(h.byToken))
	}
	r.Post("/status/{slug}", h.bySlug)
}

// bySlug serves /status/{slug}: every page but an unlisted one.
func (h *Public) bySlug(w http.ResponseWriter, r *http.Request) {
	slug, ok := statuspage.NormalizeSlug(chi.URLParam(r, "slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := store.GetStatusPageBySlug(r.Context(), h.db.Reader, slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a status page", err)
		return
	}
	if p.Visibility == statuspage.Unlisted {
		http.NotFound(w, r)
		return
	}
	h.serve(w, r, p, pageAccess{base: "/status/" + slug})
}

// byToken serves /s/{token}: an unlisted page by its secret address. The
// token is never logged: the access log records the route pattern, not
// the path (middleware.AccessLog).
func (h *Public) byToken(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	hash, ok := statuspage.HashToken(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := store.GetStatusPageByTokenHash(r.Context(), h.db.Reader, hash)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a status page", err)
		return
	}
	h.serve(w, r, p, pageAccess{base: "/s/" + token})
}

// render writes the page. Its CSP admits the page's accent style by hash
// and nothing else inline.
func (h *Public) render(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail) {
	v, err := h.view(r.Context(), p)
	if err != nil {
		h.fail(w, r, "building a status page", err)
		return
	}
	if v.AccentCSS != "" {
		w.Header().Set("Content-Security-Policy", middleware.CSPWithStyleHash(styleHash(v.AccentCSS)))
	}
	render(w, r, h.log, http.StatusOK, templates.PublicStatusPage(v, p.Theme))
}

func (h *Public) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("status page request failed", "route", r.URL.Path, "while", what, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// view returns the page's view from the cache, or builds and caches it.
func (h *Public) view(ctx context.Context, p store.StatusPageDetail) (templates.PublicPage, error) {
	now := h.now()
	if v, ok := h.cache.get(p.ID, p.UpdatedAt, now); ok {
		return v, nil
	}
	v, err := h.build(ctx, p, now)
	if err != nil {
		return v, err
	}
	h.cache.put(p.ID, p.UpdatedAt, v, now)
	return v, nil
}

// build reads the figures of a page as of now.
func (h *Public) build(ctx context.Context, p store.StatusPageDetail, now time.Time) (templates.PublicPage, error) {
	v := templates.PublicPage{
		Title: p.Title, Description: p.Description, IncidentDays: p.IncidentDays, PoweredBy: p.ShowPoweredBy,
		Generated: clockText(now, now, h.loc), GeneratedAt: rfc3339(now), AccentCSS: accentCSS(p.Accent),
		NoIndex: p.Visibility == statuspage.Unlisted,
	}
	if p.LogoPath != "" {
		v.Logo = "/uploads/" + p.LogoPath
	}

	days := stripBounds(now, h.loc)
	groupIndex := map[string]int{}
	for _, g := range p.Groups {
		groupIndex[g.ID] = len(v.Groups)
		v.Groups = append(v.Groups, templates.PublicGroup{Name: g.Name})
	}
	var ungrouped []templates.PublicRow
	var total incident.Ratio
	states := map[string]int{}
	for _, pm := range p.Monitors {
		row, adjusted, err := h.row(ctx, pm, days, now)
		if err != nil {
			return v, err
		}
		total.Up += adjusted.Up
		total.Den += adjusted.Den
		states[row.State]++
		if i, ok := groupIndex[pm.GroupID]; ok && pm.GroupID != "" {
			v.Groups[i].Rows = append(v.Groups[i].Rows, row)
		} else {
			ungrouped = append(ungrouped, row)
		}
	}
	groups := v.Groups[:0]
	for _, g := range v.Groups {
		if len(g.Rows) > 0 {
			groups = append(groups, g)
		}
	}
	v.Groups = groups
	if len(ungrouped) > 0 {
		v.Groups = append(v.Groups, templates.PublicGroup{Rows: ungrouped})
	}
	v.Uptime = total.Percent()
	v.Overall, v.OverallText = overall(states, len(p.Monitors))

	incidents, err := store.ListPageIncidents(ctx, h.db.Reader, p.ID, now.AddDate(0, 0, -p.IncidentDays), publicIncidentLimit)
	if err != nil {
		return v, err
	}
	for _, in := range incidents {
		end := now
		if in.EndedAt != nil {
			end = *in.EndedAt
		}
		pi := templates.PublicIncident{Service: in.DisplayName, Active: in.EndedAt == nil,
			Started: clockText(in.StartedAt, now, h.loc), StartedAt: rfc3339(in.StartedAt), Duration: formatSince(end.Sub(in.StartedAt))}
		for _, n := range in.Notes {
			pi.Notes = append(pi.Notes, templates.PublicNote{Message: n.Message, Time: clockText(n.At, now, h.loc), At: rfc3339(n.At)})
		}
		v.Incidents = append(v.Incidents, pi)
	}
	return v, nil
}

// row builds one service: its state, latency and uptime strip, from one
// read of its intervals over the strip's 90 days.
func (h *Public) row(ctx context.Context, pm store.StatusPageMonitor, days []time.Time, now time.Time) (templates.PublicRow, incident.Ratio, error) {
	row := templates.PublicRow{Name: pm.DisplayName}
	m, err := store.GetMonitor(ctx, h.db.Reader, pm.MonitorID)
	if err != nil {
		return row, incident.Ratio{}, err
	}
	from, to := days[0], days[len(days)-1]
	iv, err := store.GetIntervals(ctx, h.db.Reader, pm.MonitorID, from, to, now, h.loc)
	if err != nil {
		return row, incident.Ratio{}, err
	}
	row.State = displayState(m)
	if row.State != templates.StatePaused && row.State != templates.StateDown && inMaintenance(iv, now) {
		row.State = templates.StateMaintenance
	}
	if pm.ShowLatency {
		if d, ok, err := store.LastDuration(ctx, h.db.Reader, pm.MonitorID); err != nil {
			return row, incident.Ratio{}, err
		} else if ok {
			row.Latency = strconv.FormatInt(d.Milliseconds(), 10) + " ms"
		}
	}
	_, adjusted := store.UptimeOf(iv, from, to, now)
	row.Uptime = adjusted.Percent()

	counts := map[string]int{}
	for i := 0; i+1 < len(days); i++ {
		d := stripDay(iv, days[i], days[i+1], now)
		row.Days = append(row.Days, d)
		counts[d.Class]++
	}
	row.StripText = fmt.Sprintf("Last %d days: %s uptime; %d without incidents, %d with downtime, %d in maintenance, %d without data",
		stripDays, row.Uptime, counts[templates.DayUp], counts[templates.DayPartial]+counts[templates.DayDown],
		counts[templates.DayMaintenance], counts[templates.DayNoData])
	return row, adjusted, nil
}

// stripBounds returns the starts of the strip's days in loc, oldest first,
// and the start of tomorrow: stripDays+1 local midnights. Midnights are
// computed from the calendar, so a day of a daylight-saving change is 23
// or 25 hours long.
func stripBounds(now time.Time, loc *time.Location) []time.Time {
	t := now.In(loc)
	out := make([]time.Time, 0, stripDays+1)
	for i := stripDays - 1; i >= -1; i-- {
		d := t.AddDate(0, 0, -i)
		out = append(out, maintenance.Local(d.Year(), d.Month(), d.Day(), 0, 0, 0, loc))
	}
	return out
}

// stripDay is one bar: the adjusted uptime of [from, to) as of now.
func stripDay(iv store.Intervals, from, to, now time.Time) templates.PublicDay {
	raw, adjusted := store.UptimeOf(iv, from, to, now)
	n := 0
	for _, s := range iv.Down {
		end := s.To
		if end.IsZero() {
			end = now
		}
		if s.From.Before(to) && end.After(from) {
			n++
		}
	}
	date := from.Format("Mon 2 Jan 2006")
	incidents := "no incidents"
	switch {
	case n == 1:
		incidents = "1 incident"
	case n > 1:
		incidents = strconv.Itoa(n) + " incidents"
	}
	switch {
	case !raw.OK():
		return templates.PublicDay{Class: templates.DayNoData, Label: date + ": no data"}
	case !adjusted.OK():
		return templates.PublicDay{Class: templates.DayMaintenance, Label: date + ": maintenance, " + incidents}
	}
	class := templates.DayUp
	switch {
	case adjusted.Up < adjusted.Den && adjusted.Value() < dayDownBelow:
		class = templates.DayDown
	case adjusted.Up < adjusted.Den:
		class = templates.DayPartial
	}
	return templates.PublicDay{Class: class, Label: date + ": " + adjusted.Percent() + " uptime, " + incidents}
}

// inMaintenance reports whether a maintenance window covers the monitor
// now. Occurrences are clipped to the range, which ends at now, so one in
// effect ends exactly there.
func inMaintenance(iv store.Intervals, now time.Time) bool {
	for _, s := range iv.Maintenance {
		if !s.To.Before(now) && !s.From.After(now) {
			return true
		}
	}
	return false
}

// overall sums up the services' states, worst first. Paused services do
// not count.
func overall(states map[string]int, total int) (string, string) {
	counted := total - states[templates.StatePaused]
	switch {
	case counted == 0:
		return templates.OverallNone, "No services are monitored"
	case states[templates.StateDown] == counted:
		return templates.OverallMajor, "Major outage"
	case states[templates.StateDown] > 0:
		return templates.OverallPartial, "Partial outage"
	case states[templates.StatePending]+states[templates.StateFlapping] > 0:
		return templates.OverallDegraded, "Degraded performance"
	case states[templates.StateMaintenance] > 0:
		return templates.OverallMaintenance, "Under maintenance"
	}
	return templates.OverallUp, "All systems operational"
}

// accentCSS is the page's accent override, or "" for the theme's own.
// accent was checked on save (#rrggbb); anything else is ignored, so no
// stored value can inject CSS. The text on accent is black or white,
// whichever contrasts more.
func accentCSS(accent string) string {
	accent, ok := statuspage.NormalizeAccent(accent)
	if !ok || accent == "" {
		return ""
	}
	contrast := "#ffffff"
	if statuspage.Contrast(accent, "#000000") > statuspage.Contrast(accent, "#ffffff") {
		contrast = "#000000"
	}
	return "html[data-theme]{--accent:" + accent + ";--accent-contrast:" + contrast + "}"
}

// styleHash is the CSP source for an inline style with this content.
func styleHash(css string) string {
	sum := sha256.Sum256([]byte(css))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// pageCache keeps each page's view for publicCacheTTL. It holds at most
// one entry per page that exists, so it is bounded by the pages; an entry
// of a page saved since (a newer updated_at) is not used.
type pageCache struct {
	mu      sync.Mutex
	entries map[string]cachedPage
}

type cachedPage struct {
	view    templates.PublicPage
	updated time.Time
	expires time.Time
}

func (c *pageCache) get(id string, updated, now time.Time) (templates.PublicPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || !now.Before(e.expires) || !e.updated.Equal(updated) {
		return templates.PublicPage{}, false
	}
	return e.view, true
}

func (c *pageCache) put(id string, updated time.Time, v templates.PublicPage, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[id] = cachedPage{view: v, updated: updated, expires: now.Add(publicCacheTTL)}
}
