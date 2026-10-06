package web

import (
	"log/slog"
	"math"
	"net/http"
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
	// maxProblems is how many problems the strip lists; the rest is a count.
	maxProblems = 8
	// tlsWarnDays is how soon a certificate must expire to be a problem
	// (docs/36: "expires in 14 days").
	tlsWarnDays = 14
)

// Overview serves the Overview page (docs/03 "Overview"): the problem
// strip, the monitors by state and the latest incidents. Notification
// channel and system warnings join the strip with their features.
type Overview struct {
	db  *db.DB
	loc *time.Location
	log *slog.Logger
	now func() time.Time
}

// NewOverview returns the Overview handler; loc nil means UTC.
func NewOverview(d *db.DB, loc *time.Location, logger *slog.Logger) *Overview {
	if loc == nil {
		loc = time.UTC
	}
	return &Overview{db: d, loc: loc, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterOverview mounts the page and its live fragment inside
// RequireAuth.
func RegisterOverview(r chi.Router, h *Overview) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/", http.HandlerFunc(h.page))
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
	v.Monitors = len(monitors)
	v.Problems, v.MoreProblems, v.Counts = summarize(monitors, now)
	v.Incidents, err = incidentListView(ctx, h.db.Reader, h.loc, now, "", overviewIncidents)
	return v, err
}

// summarize counts the monitors by displayed state and lists what needs
// attention: monitors that are down, then flapping ones, then certificates
// that expire soon or have expired. Monitors are in name order already.
func summarize(monitors []store.Monitor, now time.Time) (problems []templates.Problem, more int, counts []templates.Count) {
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
	all := append(append(down, flapping...), tls...)
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
