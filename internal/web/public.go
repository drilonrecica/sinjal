package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/middleware"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
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
	baseURL string // SINJAL_BASE_URL without a trailing slash, "" when unset
	key     []byte // signs page cookies (PageKeyLabel)
	limiter *ratelimit.Limiter
	loc     *time.Location
	log     *slog.Logger
	now     func() time.Time
	cache   pageCache
}

// NewPublic returns the public status page handler. baseURL is
// SINJAL_BASE_URL, where authenticated pages on mapped hostnames send
// their visitors; key signs the cookies of password-protected pages; loc
// nil means UTC.
func NewPublic(d *db.DB, baseURL string, key []byte, loc *time.Location, logger *slog.Logger) *Public {
	if loc == nil {
		loc = time.UTC
	}
	return &Public{db: d, baseURL: strings.TrimSuffix(baseURL, "/"), key: key, limiter: ratelimit.New(pageMaxFailures, pageWindow, pageTrackedKeys), loc: loc,
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
	for _, f := range []struct {
		name string
		kind int
	}{{"api.json", kindJSON}, {"feed.xml", kindFeed}} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r.Method(method, "/status/{slug}/"+f.name, h.bySlugAs(f.kind))
			r.Method(method, "/s/{token}/"+f.name, h.byTokenAs(f.kind))
		}
	}
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

// render writes the page served at at. Its CSP admits the page's accent
// style by hash and nothing else inline.
func (h *Public) render(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail, at pageAccess) {
	v, err := h.view(r.Context(), p)
	if err != nil {
		h.fail(w, r, "building a status page", err)
		return
	}
	v.FeedURL = feedURL(at.base)
	if v.AccentCSS != "" {
		w.Header().Set("Content-Security-Policy", middleware.CSPWithStyleHash(styleHash(v.AccentCSS)))
	}
	render(w, r, h.log, http.StatusOK, templates.PublicStatusPage(v, p.Theme))
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

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
		Layout: p.Layout, Zone: now.In(h.loc).Format("MST"),
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
	rows := make([]templates.PublicRow, 0, len(p.Monitors))
	for _, pm := range p.Monitors {
		row, adjusted, err := h.row(ctx, p.ID, pm, days, now)
		if err != nil {
			return v, err
		}
		rows = append(rows, row)
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
	v.Services, v.Operational = len(p.Monitors), states[templates.StateUp]
	v.Overall, v.OverallText = overall(states, len(p.Monitors))
	v.Names, v.Verb = publicHeadline(v.Overall, v.Groups)
	v.HeroState = publicHeroState(v.Overall)

	incidents, err := store.ListPageIncidents(ctx, h.db.Reader, p.ID, now.AddDate(0, 0, -p.IncidentDays), publicIncidentLimit)
	if err != nil {
		return v, err
	}
	var oldest time.Time // the start of the longest ongoing incident
	for _, in := range incidents {
		end := now
		if in.EndedAt != nil {
			end = *in.EndedAt
		} else if oldest.IsZero() || in.StartedAt.Before(oldest) {
			oldest = in.StartedAt
		}
		pi := templates.PublicIncident{Key: h.opaqueKey(p.ID, "incident", in.ID), Service: in.DisplayName, Active: in.EndedAt == nil,
			Date: dateText(in.StartedAt, now, h.loc), Time: in.StartedAt.In(h.loc).Format("15:04"), StartedAt: rfc3339(in.StartedAt),
			Long: longDuration(end.Sub(in.StartedAt)), Short: shortDuration(end.Sub(in.StartedAt)), Duration: formatSince(end.Sub(in.StartedAt))}
		if in.EndedAt != nil {
			pi.EndedAt, pi.EndTime = rfc3339(*in.EndedAt), in.EndedAt.In(h.loc).Format("15:04")
		}
		for _, n := range in.Notes {
			pi.Notes = append(pi.Notes, templates.PublicNote{Message: n.Message, Time: clockText(n.At, now, h.loc), At: rfc3339(n.At)})
		}
		v.Incidents = append(v.Incidents, pi)
	}
	if n, ok := latestPublicNote(incidents); ok {
		v.Note = &templates.PublicNote{Service: n.Service, Message: n.Message, Time: clockText(n.At, now, h.loc), At: rfc3339(n.At)}
	}
	v.LastIssue = lastIssue(incidents, p.IncidentDays, now, h.loc)
	if !oldest.IsZero() && (v.Overall == templates.OverallMajor || v.Overall == templates.OverallPartial) {
		v.DownFor, v.DownSince = longDuration(now.Sub(oldest)), oldest.In(h.loc).Format("15:04")
	}
	cal := calendarOf(rows, days, incidents, now)
	v.Calendar, v.CalendarMonths, v.CalendarWeeks, v.CalendarUpcoming = cal.days, cal.months, cal.weeks, cal.upcoming
	return v, nil
}

// row builds one service: its state, latency and uptime strip, from one
// read of its intervals over the strip's 90 days.
func (h *Public) row(ctx context.Context, pageID string, pm store.StatusPageMonitor, days []time.Time, now time.Time) (templates.PublicRow, incident.Ratio, error) {
	row := templates.PublicRow{Name: pm.DisplayName, Key: h.opaqueKey(pageID, "component", pm.MonitorID)}
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
			row.LatencyMS, row.HasLatency = d.Milliseconds(), true
		}
	}
	var adjusted incident.Ratio
	row.Days, adjusted, row.StripText = stripOf(iv, days, now)
	row.Uptime = adjusted.Percent()
	since := ""
	since, row.SinceDay = sinceOf(row.Days, days)
	if since != "" {
		row.Since = "Monitoring since " + since
		row.StripText += ". " + row.Since
	}
	var downFor time.Duration
	if row.State == templates.StateDown {
		downFor = downNow(iv, now)
	}
	row.Downtime = downtimeText(adjusted, since, downFor)
	return row, adjusted, nil
}

// sinceOf is the date ("25 Oct") and index of a strip's first day with
// data when days before it have none; "" and 0 when the strip starts with
// data, or has none at all. Days without data later on (paused all day)
// stay in the strip.
func sinceOf(bars []templates.Day, days []time.Time) (string, int) {
	for i, d := range bars {
		if d.Class != templates.DayNoData {
			if i == 0 {
				return "", 0
			}
			return days[i].Format("2 Jan"), i
		}
	}
	return "", 0
}

// downNow is how long the monitor's open incident has lasted, 0 without
// one.
func downNow(iv store.Intervals, now time.Time) time.Duration {
	for _, s := range iv.Down {
		if s.To.IsZero() {
			return now.Sub(s.From)
		}
	}
	return 0
}

// downtimeText is a row's Downtime (docs/12 "Downtime in human time"):
// the adjusted downtime of the strip's days, counted from since ("25
// Oct") when the strip starts later; "Down now, for 18 min" while down
// (downFor > 0); "" without any data.
func downtimeText(adjusted incident.Ratio, since string, downFor time.Duration) string {
	switch {
	case downFor > 0:
		return "Down now, for " + shortDuration(downFor)
	case !adjusted.OK():
		return ""
	}
	span := fmt.Sprintf(" in %d days", stripDays)
	if since != "" {
		span = " since " + since
	}
	down := time.Duration(adjusted.Den-adjusted.Up) * time.Second
	if down <= 0 {
		return "No downtime" + span
	}
	return "Down " + shortDuration(down) + span
}

// shortDuration is d in the units of a downtime line: "under 1 min",
// "26 min", "1 h 5 min", "2 d 3 h".
func shortDuration(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "under 1 min"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 24*60:
		if m%60 == 0 {
			return fmt.Sprintf("%d h", m/60)
		}
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	}
	if h := m / 60 % 24; h > 0 {
		return fmt.Sprintf("%d d %d h", m/(24*60), h)
	}
	return fmt.Sprintf("%d d", m/(24*60))
}

// longDuration is d in words: "less than a minute", "12 minutes", "1
// hour 5 minutes", "2 days 3 hours".
func longDuration(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "less than a minute"
	case m < 60:
		return plural(m, "minute")
	case m < 24*60:
		if m%60 == 0 {
			return plural(m/60, "hour")
		}
		return plural(m/60, "hour") + " " + plural(m%60, "minute")
	}
	if h := m / 60 % 24; h > 0 {
		return plural(m/(24*60), "day") + " " + plural(h, "hour")
	}
	return plural(m/(24*60), "day")
}

// lastIssue is the page's LastIssue: the newest listed incident that has
// ended, in words, or the quiet line for the page's incident_days.
func lastIssue(incidents []store.PageIncident, incidentDays int, now time.Time, loc *time.Location) string {
	for _, in := range incidents {
		if in.EndedAt == nil {
			continue
		}
		return fmt.Sprintf("The last interruption was %s, %s on %s.", daysAgo(in.StartedAt, now, loc),
			longDuration(in.EndedAt.Sub(in.StartedAt)), in.DisplayName)
	}
	if incidentDays == 1 {
		return "No interruptions in the last day."
	}
	return "No interruptions in the last " + plural(incidentDays, "day") + "."
}

// localDays counts the local calendar days from t to now.
func localDays(t, now time.Time, loc *time.Location) int {
	date := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	return int(date(now).Sub(date(t)).Hours() / 24)
}

// daysAgo is the local calendar days from t to now in words: "today",
// "yesterday", "3 days ago".
func daysAgo(t, now time.Time, loc *time.Location) string {
	switch n := localDays(t, now, loc); n {
	case 0:
		return "today"
	case 1:
		return "yesterday"
	default:
		return strconv.Itoa(n) + " days ago"
	}
}

// dateText is the day of t for an incident's date column: "Today",
// "Yesterday", "Fri 23 Oct", or with the year when it is not this year's.
func dateText(t, now time.Time, loc *time.Location) string {
	switch localDays(t, now, loc) {
	case 0:
		return "Today"
	case 1:
		return "Yesterday"
	}
	t = t.In(loc)
	if t.Year() != now.In(loc).Year() {
		return t.Format("2 Jan 2006")
	}
	return t.Format("Mon 2 Jan")
}

// dayRank orders the day classes from best to worst for the calendar.
var dayRank = map[string]int{templates.DayNoData: 0, templates.DayUp: 1, templates.DayMaintenance: 2,
	templates.DayPartial: 3, templates.DayDown: 4}

// calendar is a page's Calendar with its row labels and the rest of the
// current month (templates.PublicPage).
type calendar struct {
	days, upcoming []templates.PublicDay
	months, weeks  []string
}

// calendarOf is the page's Calendar: for each of the strips' days (days
// are their bounds, stripBounds), the worst class over the services'
// strips, the listed incidents that touched it and whether every other
// service was fine; each day placed in the month and week grids
// (placeDay), with the rows' labels; and the days left in the current
// month after today. incidents are in the order of PublicPage.Incidents.
func calendarOf(rows []templates.PublicRow, days []time.Time, incidents []store.PageIncident, now time.Time) calendar {
	var cal calendar
	cal.days = make([]templates.PublicDay, len(days)-1)
	first := days[0]
	for i := range cal.days {
		d := templates.PublicDay{Class: templates.DayNoData, Date: days[i].Format("2006-01-02"), Label: days[i].Format("Mon 2 Jan"), Long: days[i].Format("Monday 2 January")}
		if i == len(cal.days)-1 {
			d.Long = "Today, " + d.Long
		}
		for _, r := range rows {
			if dayRank[r.Days[i].Class] > dayRank[d.Class] {
				d.Class = r.Days[i].Class
			}
		}
		placeDay(&d, days[i], first, i)
		if d.Month == len(cal.months) {
			cal.months = append(cal.months, days[i].Format("Jan"))
		}
		if d.Week == len(cal.weeks) {
			cal.weeks = append(cal.weeks, days[i].Format("2 Jan"))
		}
		cal.days[i] = d
	}
	for j, in := range incidents {
		end := now
		if in.EndedAt != nil {
			end = *in.EndedAt
		}
		for i := range cal.days {
			if in.StartedAt.Before(days[i+1]) && end.After(days[i]) {
				cal.days[i].Incidents = append(cal.days[i].Incidents, j)
			}
		}
	}
	for i := range cal.days {
		cal.days[i].OthersFine = othersFine(rows, i, cal.days[i].Incidents, incidents)
	}
	// The rest of the current month, from tomorrow (the last bound).
	today := days[len(days)-1].AddDate(0, 0, -1)
	for i, t := len(days)-1, days[len(days)-1]; t.Month() == today.Month(); i, t = i+1, t.AddDate(0, 0, 1) {
		d := templates.PublicDay{Date: t.Format("2006-01-02"), Label: t.Format("Mon 2 Jan"), Long: t.Format("Monday 2 January")}
		placeDay(&d, t, first, i)
		cal.upcoming = append(cal.upcoming, d)
	}
	return cal
}

// placeDay sets where day t, the i-th day from first, sits in the
// calendar's grids: its month's row and its day of the month; its week's
// row (weeks start on Monday) and its weekday.
func placeDay(d *templates.PublicDay, t, first time.Time, i int) {
	d.Month = (t.Year()*12 + int(t.Month())) - (first.Year()*12 + int(first.Month()))
	d.Dom = t.Day()
	d.Week = (i + mondayFirst(first) - 1) / 7
	d.Weekday = mondayFirst(t)
}

// mondayFirst is t's weekday counted from Monday as 1 to Sunday as 7.
func mondayFirst(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

// othersFine reports whether every service with downtime on day i is
// named by one of the day's listed incidents (idx into incidents), so the
// opened day can say "Every other service had no issues."
func othersFine(rows []templates.PublicRow, i int, idx []int, incidents []store.PageIncident) bool {
	for _, r := range rows {
		if c := r.Days[i].Class; c != templates.DayPartial && c != templates.DayDown {
			continue
		}
		named := false
		for _, j := range idx {
			if incidents[j].DisplayName == r.Name {
				named = true
				break
			}
		}
		if !named {
			return false
		}
	}
	return true
}

// feedURL is the page's feed.xml under base, the address it is served at
// (pageAccess.base).
func feedURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/feed.xml"
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

// publicHeadline is the headline's two parts (docs/12 "Headline"): the
// services that are down (or, when none is, unstable or under
// maintenance) by their public names, the first three and a count of the
// rest, and the verb that fits them. Both are "" when everything is up or
// nothing is monitored.
func publicHeadline(overall string, groups []templates.PublicGroup) (names, verb string) {
	var states []string
	switch overall {
	case templates.OverallMajor, templates.OverallPartial:
		states, verb = []string{templates.StateDown}, "down"
	case templates.OverallDegraded:
		states, verb = []string{templates.StatePending, templates.StateFlapping}, "unstable"
	case templates.OverallMaintenance:
		states, verb = []string{templates.StateMaintenance}, "under maintenance"
	default:
		return "", ""
	}
	var list []string
	for _, g := range groups {
		for _, r := range g.Rows {
			if slices.Contains(states, r.State) {
				list = append(list, r.Name)
			}
		}
	}
	if len(list) == 0 {
		return "", ""
	}
	if len(list) == 1 {
		return list[0], "is " + verb
	}
	if len(list) > 3 {
		list = append(list[:3:3], strconv.Itoa(len(list)-3)+" more")
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1], "are " + verb
}

// publicHeroState colours the hero's Ambient light (components.css keys
// it on the monitor states): an outage is down, degraded is flapping,
// everything else the calm accent.
func publicHeroState(overall string) string {
	switch overall {
	case templates.OverallMajor, templates.OverallPartial:
		return templates.StateDown
	case templates.OverallDegraded:
		return templates.StateFlapping
	}
	return templates.StateUp
}

// publicNote is the note shown under the status sentence.
type publicNote struct {
	Service, Message string
	At               time.Time
}

// latestPublicNote is the newest published note of an ongoing incident,
// for the top of the page (M8-17, owner decision): what visitors read
// first while something is wrong. Notes of ended incidents stay with
// their incident.
func latestPublicNote(incidents []store.PageIncident) (publicNote, bool) {
	var best publicNote
	found := false
	for _, in := range incidents {
		if in.EndedAt != nil {
			continue
		}
		for _, n := range in.Notes {
			if !found || n.At.After(best.At) {
				best, found = publicNote{Service: in.DisplayName, Message: n.Message, At: n.At}, true
			}
		}
	}
	return best, found
}
