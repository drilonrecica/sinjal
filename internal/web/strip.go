package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The 90-day strip of a status page service (docs/12) and of a monitor's
// detail page: one bar per local day, from the monitor's intervals, so
// both read the same uptime (docs/10 "Uptime").
const (
	// stripDays is the length of the uptime strip (docs/12, P0-15).
	stripDays = 90
	// dayDownBelow: a day under this adjusted uptime is drawn as down,
	// one at or above it with some downtime as partial.
	dayDownBelow = 0.95
)

// stripOf is the strip of a monitor whose intervals iv cover days
// (stripBounds): its bars, its adjusted uptime and the strip in words.
func stripOf(iv store.Intervals, days []time.Time, now time.Time) ([]templates.Day, incident.Ratio, string) {
	_, adjusted := store.UptimeOf(iv, days[0], days[len(days)-1], now)
	bars := make([]templates.Day, 0, len(days)-1)
	counts := map[string]int{}
	for i := 0; i+1 < len(days); i++ {
		d := stripDay(iv, days[i], days[i+1], now)
		bars = append(bars, d)
		counts[d.Class]++
	}
	text := fmt.Sprintf("Last %d days: %s uptime; %d without incidents, %d with downtime, %d in maintenance, %d without data",
		len(bars), adjusted.Percent(), counts[templates.DayUp], counts[templates.DayPartial]+counts[templates.DayDown],
		counts[templates.DayMaintenance], counts[templates.DayNoData])
	return bars, adjusted, text
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
func stripDay(iv store.Intervals, from, to, now time.Time) templates.Day {
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
		return templates.Day{Class: templates.DayNoData, Label: date + ": no data"}
	case !adjusted.OK():
		return templates.Day{Class: templates.DayMaintenance, Label: date + ": maintenance, " + incidents}
	}
	class := templates.DayUp
	switch {
	case adjusted.Up < adjusted.Den && adjusted.Value() < dayDownBelow:
		class = templates.DayDown
	case adjusted.Up < adjusted.Den:
		class = templates.DayPartial
	}
	return templates.Day{Class: class, Label: date + ": " + adjusted.Percent() + " uptime, " + incidents}
}
