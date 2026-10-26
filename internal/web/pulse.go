package web

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The Heartline pulse (docs/04 "Response pulse", docs/12 "Heartline"): a
// service's last 24 hours from store.LatencyHistory in 96 buckets, drawn
// on the server as SVG paths in a fixed viewBox. The stroke does not
// scale, so the browser stretches only the geometry to the pulse's box.
const (
	pulseHours   = 24
	pulseBuckets = 96
	pulseWidth   = 960 // 10 per bucket
	pulseHeight  = 64
	pulsePad     = 2 // above the highest point and below the floor
	pulseFloor   = pulseHeight - pulsePad
	// pulseHeadroom scales the highest average so the peak sits under the
	// top edge.
	pulseHeadroom = 1.15
)

// pulseOf draws the pulse of the buckets over [from, to): the line through
// the average response time of each bucket with a sample, at the bucket's
// centre, broken where a bucket has none; the area under each run; the
// dot at the line's end; and, when the service is down (downSince is set),
// the floor from that moment to now as the tail, with the dot at its end.
// The text sums up the range: min and max from the summary, the median the
// middle bucket's average.
func pulseOf(points []store.LatencyPoint, stats store.LatencyStats, from, to, downSince time.Time) templates.Pulse {
	p := templates.Pulse{Text: "No response data in the last 24 hours."}
	span := to.Sub(from)
	x := func(t time.Time) float64 { return float64(t.Sub(from)) / float64(span) * pulseWidth }

	var peak time.Duration
	var avgs []time.Duration
	for _, pt := range points {
		if pt.Samples > 0 {
			peak = max(peak, pt.Avg)
			avgs = append(avgs, pt.Avg)
		}
	}
	if len(avgs) > 0 {
		slices.Sort(avgs)
		p.Text = fmt.Sprintf("Response over 24 hours: min %s, median %s, max %s", msText(stats.Min), msText(avgs[len(avgs)/2]), msText(stats.Max))
	}
	scale := float64(peak) * pulseHeadroom
	if scale == 0 {
		scale = 1
	}
	y := func(d time.Duration) float64 { return pulseFloor - float64(d)/scale*(pulseFloor-pulsePad) }

	var line, area strings.Builder
	var endX, endY string
	floor := num(pulseFloor)
	prev := -2 // the index of the last drawn bucket; -2 so the first bucket (0 or later) starts a run
	for _, pt := range points {
		if pt.Samples == 0 {
			continue
		}
		// Integer arithmetic: a bucket on the quarter hour must not land a
		// hair under its index.
		i := min(int(int64(pt.At.Sub(from))*pulseBuckets/int64(span)), pulseBuckets-1)
		px, py := num(float64(i*(pulseWidth/pulseBuckets)+pulseWidth/pulseBuckets/2)), num(y(pt.Avg))
		if i == prev+1 {
			line.WriteString("L" + px + " " + py)
			area.WriteString("L" + px + " " + py)
		} else {
			// A new run: close the area of the last one, then start both.
			if endX != "" {
				area.WriteString("L" + endX + " " + floor + "Z")
			}
			line.WriteString("M" + px + " " + py)
			area.WriteString("M" + px + " " + floor + "L" + px + " " + py)
		}
		endX, endY, prev = px, py, i
	}
	if endX != "" {
		area.WriteString("L" + endX + " " + floor + "Z")
	}
	p.Line, p.Area = line.String(), area.String()
	if endX != "" {
		p.End = "M" + endX + " " + endY + "h0"
	}
	if !downSince.IsZero() {
		start := max(x(downSince), 0)
		p.Tail = "M" + num(start) + " " + num(pulseFloor) + "H" + num(pulseWidth)
		p.End = "M" + num(pulseWidth) + " " + num(pulseFloor) + "h0"
	}
	return p
}

// num is a path coordinate with at most one decimal.
func num(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// msText is d in whole milliseconds, "71 ms".
func msText(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
}

// medianLatency is the page's "Response, median": the median of the last
// response times of the services that show latency and are not down (a
// down service has not replied) or paused; "" without any. With an even
// count it is the mean of the two middle values.
func medianLatency(groups []templates.PublicGroup) string {
	var ms []int64
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.HasLatency && r.State != templates.StateDown && r.State != templates.StatePaused {
				ms = append(ms, r.LatencyMS)
			}
		}
	}
	n := len(ms)
	if n == 0 {
		return ""
	}
	slices.Sort(ms)
	m := ms[n/2]
	if n%2 == 0 {
		m = (ms[n/2-1] + ms[n/2]) / 2
	}
	return strconv.FormatInt(m, 10) + " ms"
}
