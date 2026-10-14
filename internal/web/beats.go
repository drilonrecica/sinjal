package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Beats rendered per bar. The bar clips what its width cannot show
// (docs/04), so these are the most a list row or the detail hero can use.
const (
	listBeats   = 36
	detailBeats = recent.Cap
)

// barKind is which beat bar a view gets: a list row's, or the detail
// hero's, which keyboard users can move through and which carries its
// tips. A row's tips are fetched on the first hover (beatTips): written
// into 1,000 rows they cost 17 of the list's 50 ms and 1.6 MB (docs/18).
type barKind struct {
	n    int
	hero bool
}

var (
	rowBar  = barKind{listBeats, false}
	heroBar = barKind{detailBeats, true}
)

// beatClock writes beat tips. One is made per request: the list writes
// 36,000 of them, so the day and year boundaries are found once and each
// tip is appended into one reused buffer.
type beatClock struct {
	loc        *time.Location
	today, jan int64 // Unix seconds of the local midnight starting today and of 1 January
	buf        []byte
}

func newBeatClock(now time.Time, loc *time.Location) *beatClock {
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	jan := time.Date(n.Year(), time.January, 1, 0, 0, 0, 0, loc)
	return &beatClock{loc: loc, today: today.Unix(), jan: jan.Unix(), buf: make([]byte, 0, 64)}
}

// when is the time of a check to the second (beats can be seconds apart),
// with the day when it is not today and the year when not this year.
func (k *beatClock) when(buf []byte, at int64) []byte {
	t := time.Unix(at, 0).In(k.loc)
	switch {
	case at >= k.today:
		return t.AppendFormat(buf, "15:04:05")
	case at >= k.jan:
		return t.AppendFormat(buf, "Mon 2 Jan, 15:04:05")
	}
	return t.AppendFormat(buf, "2 Jan 2006, 15:04:05")
}

// tip is "11:59:30 · 128 ms · Up", or "… · Down: Timeout" for a failure.
func (k *beatClock) tip(c recent.Check) string {
	b := append(k.when(k.buf[:0], c.At), " · "...)
	b = append(b, formatLatency(time.Duration(c.MS)*time.Millisecond)...)
	switch kind := failureKinds[c.Kind]; {
	case c.OK:
		b = append(b, " · Up"...)
	case kind != "":
		b = append(append(b, " · Down: "...), kind...)
	default:
		b = append(b, " · Down"...)
	}
	k.buf = b
	return string(b)
}

// beatBar turns a monitor's checks, newest first, into a bar of kind k.
func beatBar(id string, checks []recent.Check, k barKind, clock *beatClock) templates.BeatBarView {
	checks = checks[:min(len(checks), k.n)]
	b := templates.BeatBarView{Beats: make([]templates.Beat, k.n), Focusable: k.hero, Label: "No checks yet."}
	up := 0
	for i, c := range checks {
		b.Beats[i].Result = templates.BeatDown
		if c.OK {
			up++
			b.Beats[i].Result = templates.BeatUp
		}
		if k.hero {
			b.Beats[i].Tip = clock.tip(c)
		}
	}
	if len(checks) > 0 {
		if !k.hero {
			b.TipsHref = "/fragments/monitors/" + id + "/beats?until=" + strconv.FormatInt(checks[0].At, 10)
		}
		latest := "down"
		if checks[0].OK {
			latest = "up"
		}
		b.Newest = checks[0].At
		b.Label = "Last " + strconv.Itoa(len(checks)) + " checks: " + strconv.Itoa(up) + " up, " +
			strconv.Itoa(len(checks)-up) + " down. Latest: " + latest + " at " + string(clock.when(nil, checks[0].At)) + "."
	}
	return b
}

// beatTips serves GET /fragments/monitors/{id}/beats?until=…: the tips of a
// list row's beats as a JSON array, newest first, from the checks that
// started at or before until (the bar's newest), so they line up with the
// beats on the page even when more have arrived since. From memory only.
func (h *Monitors) beatTips(w http.ResponseWriter, r *http.Request) {
	until, err := strconv.ParseInt(r.URL.Query().Get("until"), 10, 64)
	if err != nil {
		http.Error(w, "bad until", http.StatusBadRequest)
		return
	}
	clock := newBeatClock(h.now(), h.loc)
	tips := make([]string, 0, listBeats)
	for _, c := range h.engine.Recent().Newest(chi.URLParam(r, "id"), recent.Cap, nil) {
		if c.At <= until && len(tips) < listBeats {
			tips = append(tips, clock.tip(c))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(tips); err != nil {
		h.log.Debug("beat tips not sent", "error", err)
	}
}
