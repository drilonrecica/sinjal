package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/history"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The History tab's chart, availability timeline and their text summary
// (docs/04 "Charts", docs/22: charts come with their values in text). The
// latency chart is drawn by uPlot from JSON in data attributes; the
// timeline and the header sparkline are SVG rendered here and need no
// script.

// chartBuckets is the most points the latency chart draws.
const chartBuckets = 720

// sparklinePoints is how many recent results the header sparkline shows.
const sparklinePoints = 30

// historyView builds the History tab for the range asked for in the query.
//
// With selector the tab offers the range selector; the Overview tab shows
// the default range, whatever the query says, and no selector.
func (h *Monitors) historyView(r *http.Request, m store.Monitor, selector bool) (templates.HistoryView, error) {
	ctx, now := r.Context(), h.now()
	query := r.URL.Query()
	if !selector {
		query = nil
	}
	rg := history.Parse(query, now, h.loc)
	stats, points, err := store.LatencyHistory(ctx, h.db.Reader, m.ID, rg.From, rg.To, chartBuckets)
	if err != nil {
		return templates.HistoryView{}, err
	}
	iv, err := store.GetIntervals(ctx, h.db.Reader, m.ID, rg.From, rg.To, now, h.loc)
	if err != nil {
		return templates.HistoryView{}, err
	}
	raw, adjusted := store.UptimeOf(iv, rg.From, rg.To, now)
	over := rangeLabel(rg, h.loc, now)
	v := templates.HistoryView{
		Range:      over,
		RangeError: rg.Error,
		HasData:    stats.Checks > 0,
		Uptime:     raw.Percent(),
		Adjusted:   adjusted.Percent(),
		Timeline:   timeline(rg.From, rg.To, now, iv, h.loc),
		From:       h.stamp(rg.From, now),
		To:         h.stamp(rg.To, now),
		Zone:       h.loc.String(),
	}
	if selector {
		v.Selector, v.MonitorID = true, m.ID
		const local = "2006-01-02T15:04"
		v.FromValue, v.ToValue, v.MaxValue = rg.From.In(h.loc).Format(local), rg.To.In(h.loc).Format(local), now.In(h.loc).Format(local)
		for _, p := range history.Presets {
			v.Presets = append(v.Presets, templates.RangeOption{Label: p.Label,
				Href: "/monitors/" + m.ID + "?tab=history&range=" + p.Key, Current: rg.Preset == p.Key})
		}
	}
	if v.HasData {
		v.Stats = latencyFacts(stats)
		v.Series, v.Overlays = chartJSON(points, rg.From, rg.To, now, iv)
	}
	v.Summary = historySummary(over, stats, iv, rg.From, rg.To, now, raw, adjusted)
	return v, nil
}

// rangeLabel names a range: "the last 24 hours", or its local start and end.
func rangeLabel(rg history.Range, loc *time.Location, now time.Time) string {
	for _, p := range history.Presets {
		if p.Key == rg.Preset {
			return "the last " + p.Label
		}
	}
	return rg.From.In(loc).Format("2 Jan 2006 15:04") + " to " + rg.To.In(loc).Format("2 Jan 2006 15:04 MST")
}

// stamp is a moment for an axis label, in the instance time zone.
func (h *Monitors) stamp(t, now time.Time) string {
	t = t.In(h.loc)
	if t.Year() == now.In(h.loc).Year() {
		return t.Format("2 Jan 15:04")
	}
	return t.Format("2 Jan 2006 15:04")
}

func latencyFacts(s store.LatencyStats) []templates.Fact {
	if s.Samples == 0 {
		return []templates.Fact{{Label: "Checks", Value: strconv.Itoa(s.Checks)}, {Label: "Failed", Value: strconv.Itoa(s.Failures)}}
	}
	return []templates.Fact{
		{Label: "Current", Value: formatLatency(s.Current)},
		{Label: "Average", Value: formatLatency(s.Avg)},
		{Label: p95Label(s), Value: formatLatency(s.P95)},
		{Label: "Min", Value: formatLatency(s.Min)},
		{Label: "Max", Value: formatLatency(s.Max)},
		{Label: "Checks", Value: strconv.Itoa(s.Checks)},
		{Label: "Failed", Value: strconv.Itoa(s.Failures)},
	}
}

// p95Label names the percentile, saying so when it is estimated from
// rolled-up buckets (docs/09 "p95": no SLA-grade precision implied).
func p95Label(s store.LatencyStats) string {
	if s.Approximate {
		return "p95 (approximate)"
	}
	return "p95"
}

// clip ends open spans at now and keeps the parts within [from, to).
func clip(spans []incident.Span, from, to, now time.Time) []incident.Span {
	var out []incident.Span
	for _, s := range spans {
		if s.To.IsZero() {
			s.To = now
		}
		s.From, s.To = later(s.From, from), earlier(s.To, to)
		if s.To.After(s.From) {
			out = append(out, s)
		}
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// chartJSON is the latency series and the overlays for chart.js, as
// unix seconds and milliseconds. A bucket without a successful check has
// no latency (null), so the line breaks there.
func chartJSON(points []store.LatencyPoint, from, to, now time.Time, iv store.Intervals) (series, overlays string) {
	type seriesJSON struct {
		T    []int64    `json:"t"`
		Avg  []*float64 `json:"avg"`
		Max  []*float64 `json:"max"`
		Fail []int      `json:"fail"`
	}
	var s seriesJSON
	ms := func(d time.Duration) *float64 {
		v := math.Round(float64(d)/float64(time.Millisecond)*10) / 10
		return &v
	}
	for _, p := range points {
		s.T = append(s.T, p.At.Unix())
		s.Fail = append(s.Fail, p.Failures)
		if p.Samples > 0 {
			s.Avg, s.Max = append(s.Avg, ms(p.Avg)), append(s.Max, ms(p.Max))
		} else {
			s.Avg, s.Max = append(s.Avg, nil), append(s.Max, nil)
		}
	}
	pairs := func(spans []incident.Span) [][2]int64 {
		out := [][2]int64{}
		for _, sp := range clip(spans, from, to, now) {
			out = append(out, [2]int64{sp.From.Unix(), sp.To.Unix()})
		}
		return out
	}
	marks := []int64{}
	for _, d := range iv.Down {
		if !d.From.Before(from) && d.From.Before(to) {
			marks = append(marks, d.From.Unix())
		}
	}
	o := struct {
		From   int64      `json:"from"`
		To     int64      `json:"to"`
		Down   [][2]int64 `json:"down"`
		Maint  [][2]int64 `json:"maint"`
		Paused [][2]int64 `json:"paused"`
		Marks  []int64    `json:"marks"`
	}{from.Unix(), to.Unix(), pairs(iv.Down), pairs(iv.Maintenance), pairs(iv.Paused), marks}
	sb, _ := json.Marshal(s)
	ob, _ := json.Marshal(o)
	return string(sb), string(ob)
}

// Timeline classes, in order of precedence where they overlap.
const (
	tlNone   = "none"   // before the monitor existed
	tlPaused = "paused" // not observed
	tlDown   = "down"
	tlMaint  = "maintenance"
	tlUp     = "up"
)

var tlLabels = map[string]string{tlNone: "No data", tlPaused: "Paused", tlDown: "Down", tlMaint: "Maintenance", tlUp: "Up"}

// timeline splits [from, to) into segments of one state each, scaled to a
// width of 1000. Time after now is left out.
func timeline(from, to, now time.Time, iv store.Intervals, loc *time.Location) []templates.TimelineSegment {
	to = earlier(to, now)
	if !to.After(from) {
		return nil
	}
	paused, down, maint := clip(iv.Paused, from, to, now), clip(iv.Down, from, to, now), clip(iv.Maintenance, from, to, now)
	cuts := []time.Time{from, to}
	if iv.Created.After(from) && iv.Created.Before(to) {
		cuts = append(cuts, iv.Created)
	}
	for _, list := range [][]incident.Span{paused, down, maint} {
		for _, s := range list {
			cuts = append(cuts, s.From, s.To)
		}
	}
	slices.SortFunc(cuts, func(a, b time.Time) int { return a.Compare(b) })
	cuts = slices.CompactFunc(cuts, func(a, b time.Time) bool { return a.Equal(b) })
	in := func(list []incident.Span, t time.Time) bool {
		return slices.ContainsFunc(list, func(s incident.Span) bool { return !t.Before(s.From) && t.Before(s.To) })
	}
	total := float64(to.Sub(from))
	var out []templates.TimelineSegment
	var starts []time.Time
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		class := tlUp
		switch {
		case a.Before(iv.Created):
			class = tlNone
		case in(paused, a):
			class = tlPaused
		case in(down, a):
			class = tlDown
		case in(maint, a):
			class = tlMaint
		}
		x, w := float64(a.Sub(from))/total*1000, float64(b.Sub(a))/total*1000
		if n := len(out); n > 0 && out[n-1].Class == class {
			out[n-1].W += w
		} else {
			out = append(out, templates.TimelineSegment{X: x, W: w, Class: class})
			starts = append(starts, a)
		}
	}
	for i := range out {
		a := starts[i]
		b := to
		if i+1 < len(starts) {
			b = starts[i+1]
		}
		out[i].Title = fmt.Sprintf("%s: %s to %s (%s)", tlLabels[out[i].Class],
			a.In(loc).Format("2 Jan 15:04"), b.In(loc).Format("2 Jan 15:04"), formatSince(b.Sub(a)))
	}
	return out
}

// historySummary is the chart and timeline in one sentence or two, for
// screen readers and for anyone who would rather read than look.
func historySummary(over string, s store.LatencyStats, iv store.Intervals, from, to, now time.Time, raw, adjusted incident.Ratio) string {
	var b strings.Builder
	switch {
	case s.Checks == 0:
		fmt.Fprintf(&b, "No checks over %s.", over)
	case s.Samples == 0:
		fmt.Fprintf(&b, "Over %s, %s, all failed.", over, plural(s.Checks, "check"))
	default:
		fmt.Fprintf(&b, "Latency over %s: current %s, average %s, %s %s, max %s, from %s, %d failed.",
			over, formatLatency(s.Current), formatLatency(s.Avg), p95Label(s), formatLatency(s.P95), formatLatency(s.Max),
			plural(s.Checks, "check"), s.Failures)
	}
	down := clip(iv.Down, from, to, now)
	var downFor time.Duration
	for _, d := range down {
		downFor += d.To.Sub(d.From)
	}
	switch len(down) {
	case 0:
		b.WriteString(" No outages.")
	default:
		fmt.Fprintf(&b, " %s, down %s in total.", plural(len(down), "outage"), formatSince(downFor))
	}
	if n := len(clip(iv.Maintenance, from, to, now)); n > 0 {
		fmt.Fprintf(&b, " %s.", plural(n, "maintenance window"))
	}
	if raw.OK() {
		fmt.Fprintf(&b, " Uptime %s, adjusted %s.", raw.Percent(), adjusted.Percent())
	}
	return b.String()
}

// sparkline is the SVG of recent durations, 100 wide and 24 high: a line
// through successful checks and a tick for each failure.
func sparkline(durations []float64) templates.Sparkline {
	var sp templates.Sparkline
	if len(durations) < 2 {
		return sp
	}
	top := 0.0
	for _, d := range durations {
		top = max(top, d)
	}
	if top <= 0 {
		top = 1
	}
	step := 100 / float64(len(durations)-1)
	var pts []string
	var ok []float64
	for i, d := range durations {
		x := float64(i) * step
		if d < 0 {
			sp.Failures = append(sp.Failures, math.Round(x*10)/10)
			continue
		}
		ok = append(ok, d)
		pts = append(pts, strconv.FormatFloat(x, 'f', 1, 64)+","+strconv.FormatFloat(22-d/top*20, 'f', 1, 64))
	}
	sp.Points = strings.Join(pts, " ")
	if len(ok) > 0 {
		slices.Sort(ok)
		sp.Label = fmt.Sprintf("Latency of the last %d checks: %s to %s", len(durations),
			formatLatency(time.Duration(ok[0]*float64(time.Millisecond))), formatLatency(time.Duration(ok[len(ok)-1]*float64(time.Millisecond))))
	} else {
		sp.Label = fmt.Sprintf("The last %d checks failed", len(durations))
	}
	if n := len(sp.Failures); n > 0 && len(ok) > 0 {
		sp.Label += fmt.Sprintf(", %d failed", n)
	}
	return sp
}
