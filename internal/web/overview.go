package web

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
	// overviewIncidents is how many ended incidents the Overview lists
	// besides the active ones.
	overviewIncidents = 5
	// overviewEvents is how many timeline entries Recent events lists.
	overviewEvents = 10
	// detailIncidents and detailEvents are what a monitor's Overview tab
	// lists beside its charts.
	detailIncidents = 5
	detailEvents    = 8
	// maxProblems is how many problems the strip lists; the rest is a count.
	maxProblems = 8
	// tlsWarnDays is how soon a certificate must expire to be a problem
	// (docs/36: "expires in 14 days").
	tlsWarnDays = 14
)

// Overview serves Home (docs/03 "Overview (Home)"): the status sentence,
// the problem strip, the monitors by state, the latest incidents and the
// latest incident events. Disk and database warnings join the strip with
// M9.
type Overview struct {
	db      *db.DB
	loc     *time.Location
	warning func() string // the engine's system warning, "" when all is well
	log     *slog.Logger
	now     func() time.Time
}

// NewOverview returns the Overview handler; loc nil means UTC, warning nil
// means there is never a system warning.
func NewOverview(d *db.DB, loc *time.Location, warning func() string, logger *slog.Logger) *Overview {
	if loc == nil {
		loc = time.UTC
	}
	if warning == nil {
		warning = func() string { return "" }
	}
	return &Overview{db: d, loc: loc, warning: warning, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterOverview mounts the page and its live fragment inside
// RequireAuth.
func RegisterOverview(r chi.Router, h *Overview) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/", http.HandlerFunc(h.page))
		r.Method(method, "/monitors", http.HandlerFunc(h.listView))
		r.Method(method, "/fragments/overview", http.HandlerFunc(h.fragment))
	}
}

func (h *Overview) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("overview failed", "route", r.URL.Path, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *Overview) page(w http.ResponseWriter, r *http.Request) {
	v, err := h.view(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.OverviewPage(pageFor(r, "Overview — Sinjal"), v))
}

// listView serves GET /monitors: Home in the pane, marked as the list view,
// so a phone shows the monitor list column there instead (docs/03 "Phones").
func (h *Overview) listView(w http.ResponseWriter, r *http.Request) {
	v, err := h.view(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page := pageFor(r, "Monitors — Sinjal")
	page.View = templates.ViewList
	render(w, r, h.log, http.StatusOK, templates.OverviewPage(page, v))
}

func (h *Overview) fragment(w http.ResponseWriter, r *http.Request) {
	v, err := h.view(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.OverviewBody(v))
}

func (h *Overview) view(r *http.Request) (templates.OverviewView, error) {
	ctx, now := r.Context(), h.now()
	v := templates.OverviewView{Admin: isAdmin(r)}
	monitors, err := store.ListMonitors(ctx, h.db.Reader)
	if err != nil {
		return v, err
	}
	channels, err := store.ListChannels(ctx, h.db.Reader)
	if err != nil {
		return v, err
	}
	v.Monitors = len(monitors)
	v.Sentence, v.SentenceState = statusSentence(monitors)
	v.Problems, v.MoreProblems, v.Counts = summarize(monitors, channels, h.warning(), v.Admin, now)
	if v.Incidents, err = incidentListView(ctx, h.db.Reader, h.loc, now, "", overviewIncidents); err != nil {
		return v, err
	}
	v.Events, err = recentEventViews(ctx, h.db.Reader, "", overviewEvents, now, h.loc)
	return v, err
}

// summarize counts the monitors by displayed state and lists what needs
// attention: the system warning (results not being saved), monitors that
// are down, flapping ones, notification channels that fail, then
// certificates that expire soon or have expired. Monitors are in name
// order already. Only admins see the warning in the database's own words.
func summarize(monitors []store.Monitor, channels []store.Channel, warning string, admin bool, now time.Time) (problems []templates.Problem, more int, counts []templates.Count) {
	n := map[string]int{}
	var down, flapping, tls []templates.Problem
	for _, m := range monitors {
		state := displayState(m)
		n[state]++
		href := "/monitors/" + m.ID
		switch state {
		case templates.StateDown:
			down = append(down, templates.Problem{State: state, Href: href,
				Text: m.Name + " is down, for " + formatSince(now.Sub(m.StateSince))})
		case templates.StateFlapping:
			flapping = append(flapping, templates.Problem{State: state, Href: href, Text: m.Name + " is flapping"})
		}
		if m.TLSNotAfter != nil && m.Enabled {
			days := int(math.Floor(m.TLSNotAfter.Sub(now).Hours() / 24))
			switch {
			case days < 0:
				tls = append(tls, templates.Problem{State: "tls", Href: href, Text: "The certificate of " + m.Name + " has expired"})
			case days < tlsWarnDays:
				tls = append(tls, templates.Problem{State: "tls", Href: href, Text: "The certificate of " + m.Name + " expires in " + plural(days, "day")})
			}
		}
	}
	var all []templates.Problem
	if warning != "" {
		text := "Check results are not being saved"
		if admin {
			text += ": " + warning
		}
		all = append(all, templates.Problem{State: "system", Text: text})
	}
	all = append(append(all, down...), flapping...)
	for _, c := range channels {
		switch {
		case !c.Enabled:
		case c.HealthState == "failed":
			all = append(all, templates.Problem{State: "channel", Href: "/notifications", Text: "Notifications to " + c.Name + " are failing"})
		case c.HealthState == "warning":
			all = append(all, templates.Problem{State: "channel", Href: "/notifications", Text: "A notification to " + c.Name + " failed recently"})
		}
	}
	all = append(all, tls...)
	if len(all) > maxProblems {
		more, all = len(all)-maxProblems, all[:maxProblems]
	}
	counts = []templates.Count{{Label: "Monitors", Value: len(monitors)}}
	for _, s := range []struct{ state, label string }{
		{templates.StateUp, "Up"}, {templates.StateDown, "Down"}, {templates.StateFlapping, "Flapping"},
		{templates.StatePending, "Pending"}, {templates.StatePaused, "Paused"},
	} {
		counts = append(counts, templates.Count{Label: s.label, Value: n[s.state], State: s.state})
	}
	return all, more, counts
}

// recentEventViews are the newest incident timeline entries of one
// monitor, or of all with monitorID "", as Recent events lists them.
func recentEventViews(ctx context.Context, q *sql.DB, monitorID string, limit int, now time.Time, loc *time.Location) ([]templates.RecentEvent, error) {
	events, err := store.RecentIncidentEvents(ctx, q, monitorID, limit)
	var out []templates.RecentEvent
	for _, e := range events {
		label, ok := eventLabels[e.Type]
		if !ok {
			label = e.Type
		}
		out = append(out, templates.RecentEvent{
			Label: label, Monitor: e.MonitorName, MonitorHref: "/monitors/" + e.MonitorID,
			IncidentHref: "/incidents/" + e.IncidentID, Time: clockText(e.At, now, loc), At: rfc3339(e.At),
		})
	}
	return out, err
}

// statusSentence is the first thing Home says (docs/03): the root cause of
// an outage and how many more monitors are affected, or that everything is
// calm. state is the hero's state for its Ambient light: down, flapping or
// up. Monitors are in name order, so the sentence is stable.
func statusSentence(monitors []store.Monitor) (text, state string) {
	byID := make(map[string]string, len(monitors))
	n := map[string]int{}
	var down, flapping []store.Monitor
	for _, m := range monitors {
		s := displayState(m)
		byID[m.ID] = s
		n[s]++
		switch s {
		case templates.StateDown:
			down = append(down, m)
		case templates.StateFlapping:
			flapping = append(flapping, m)
		}
	}
	affected := func(others int) string {
		switch others {
		case 0:
			return ""
		case 1:
			return " 1 more monitor is affected."
		}
		return " " + strconv.Itoa(others) + " more monitors are affected."
	}
	switch {
	case len(down) > 0:
		// The root cause: a down monitor whose parent is not down.
		root := down[0]
		for _, m := range down {
			if byID[m.ParentMonitorID] != templates.StateDown {
				root = m
				break
			}
		}
		return root.Name + " is down." + affected(len(down)+len(flapping)-1), templates.StateDown
	case len(flapping) == 1:
		return flapping[0].Name + " is flapping.", templates.StateFlapping
	case len(flapping) > 1:
		return strconv.Itoa(len(flapping)) + " monitors are flapping.", templates.StateFlapping
	}

	up, pending, paused := n[templates.StateUp], n[templates.StatePending], n[templates.StatePaused]
	if len(monitors) == 1 {
		m := monitors[0]
		switch byID[m.ID] {
		case templates.StatePaused:
			return m.Name + " is paused.", templates.StateUp
		case templates.StatePending:
			return m.Name + " is waiting for its first check.", templates.StateUp
		}
		return m.Name + " is up.", templates.StateUp
	}
	switch {
	case up == len(monitors):
		return "All " + strconv.Itoa(up) + " monitors are up.", templates.StateUp
	case paused == len(monitors):
		return "All " + strconv.Itoa(paused) + " monitors are paused.", templates.StateUp
	case up == 0 && paused == 0:
		return strconv.Itoa(pending) + " monitors are waiting for their first check.", templates.StateUp
	}
	text = plural(up, "monitor") + " is up"
	if up != 1 {
		text = plural(up, "monitor") + " are up"
	}
	var rest []string
	if pending > 0 {
		their := "their"
		if pending == 1 {
			their = "its"
		}
		rest = append(rest, strconv.Itoa(pending)+" "+isAre(pending)+" waiting for "+their+" first check")
	}
	if paused > 0 {
		rest = append(rest, strconv.Itoa(paused)+" "+isAre(paused)+" paused")
	}
	return text + "; " + strings.Join(rest, ", ") + ".", templates.StateUp
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
