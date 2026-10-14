package web

import (
	"net/http"
	"slices"
	"time"

	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
	// activityEvents is how many incident events Live activity merges in.
	activityEvents = 20
	// activityLines is how many lines it shows.
	activityLines = 40
)

// activity serves GET /fragments/activity: the Live activity column
// (docs/03, from 2200 px). The newest stored checks of all monitors come
// from the engine's memory, the newest incident events from the database;
// both are merged newest first. Messages are left out, as on Home: they
// can name internal hosts.
func (h *Monitors) activity(w http.ResponseWriter, r *http.Request) {
	ctx, now := r.Context(), h.now()
	checks := h.engine.Recent().Feed(make([]recent.Entry, 0, recent.FeedCap))
	events, err := store.RecentIncidentEvents(ctx, h.db.Reader, "", activityEvents)
	if err != nil {
		h.log.Error("activity failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	ids := make([]string, 0, len(checks))
	for _, c := range checks {
		ids = append(ids, c.MonitorID)
	}
	names, err := store.MonitorNames(ctx, h.db.Reader, ids)
	if err != nil {
		h.log.Error("activity failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.ActivityList(activityLinesOf(checks, events, names, now, h.loc)))
}

// activityLinesOf merges checks and incident events, newest first, at most
// activityLines. A check of a monitor that no longer exists is left out.
func activityLinesOf(checks []recent.Entry, events []store.RecentEvent, names map[string]string, now time.Time, loc *time.Location) []templates.ActivityLine {
	type line struct {
		at time.Time
		v  templates.ActivityLine
	}
	clock := newBeatClock(now, loc)
	var all []line
	for _, c := range checks {
		name, ok := names[c.MonitorID]
		if !ok {
			continue
		}
		at := time.Unix(c.Check.At, 0)
		what := "Up"
		state := templates.BeatUp
		if !c.Check.OK {
			state = templates.BeatDown
			what = "Down"
			if kind := failureKinds[c.Check.Kind]; kind != "" {
				what = "Down: " + kind
			}
		}
		all = append(all, line{at, templates.ActivityLine{
			State: state, Monitor: name, Href: "/monitors/" + c.MonitorID, What: what,
			Detail: formatLatency(time.Duration(c.Check.MS) * time.Millisecond),
			Time:   string(clock.when(nil, c.Check.At)), At: rfc3339(at),
		}})
	}
	for _, e := range events {
		label, ok := eventLabels[e.Type]
		if !ok {
			label = e.Type
		}
		all = append(all, line{e.At, templates.ActivityLine{
			State: "event", Monitor: e.MonitorName, Href: "/incidents/" + e.IncidentID, What: label,
			Time: string(clock.when(nil, e.At.Unix())), At: rfc3339(e.At),
		}})
	}
	slices.SortStableFunc(all, func(a, b line) int { return b.at.Compare(a.at) })
	out := make([]templates.ActivityLine, 0, min(len(all), activityLines))
	for _, l := range all[:min(len(all), activityLines)] {
		out = append(out, l.v)
	}
	return out
}
