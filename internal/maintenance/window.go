// Package maintenance decides when maintenance windows are in effect
// (docs/10_INCIDENTS.md "Maintenance"). It is pure: windows come from the
// store, the time zone from the configuration, the time from the caller.
package maintenance

import (
	"slices"
	"strings"
	"time"
)

// Recurrence is how a window repeats.
type Recurrence string

const (
	None   Recurrence = "none"   // once, at Start
	Daily  Recurrence = "daily"  // every day at Start's local time of day
	Weekly Recurrence = "weekly" // on the weekdays in Weekdays, at that time
)

// Window is one maintenance window.
type Window struct {
	ID         string
	Name       string
	Start      time.Time // the first occurrence; for a recurring window also its time of day
	Duration   time.Duration
	Recurrence Recurrence
	// Weekdays has bit n set for time.Weekday(n) (Sunday is bit 0). Only
	// weekly windows use it.
	Weekdays      uint8
	Suppress      bool // suppress notifications while in effect
	ExcludeUptime bool // excluded from adjusted uptime
	Scope         Scope
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Scope is which monitors a window covers: those listed and those
// carrying one of the tags. An empty scope covers every monitor.
type Scope struct {
	Monitors []string `json:"monitors,omitempty"` // monitor ids
	Tags     []string `json:"tags,omitempty"`     // tag names, compared case-insensitively
}

// All reports whether the scope covers every monitor.
func (s Scope) All() bool { return len(s.Monitors) == 0 && len(s.Tags) == 0 }

// Covers reports whether a monitor with the given tags is in scope.
func (s Scope) Covers(monitorID string, tags []string) bool {
	if s.All() || slices.Contains(s.Monitors, monitorID) {
		return true
	}
	for _, t := range tags {
		if slices.ContainsFunc(s.Tags, func(x string) bool { return strings.EqualFold(x, t) }) {
			return true
		}
	}
	return false
}

// Interval is the time from From up to, not including, To.
type Interval struct{ From, To time.Time }

// Occurrences returns the times the window is in effect within [from, to),
// clipped to it, in order, overlapping occurrences merged. A recurring
// window keeps its local time of day in loc across daylight-saving
// changes; its duration is elapsed time.
func (w Window) Occurrences(from, to time.Time, loc *time.Location) []Interval {
	if !to.After(from) || w.Duration <= 0 {
		return nil
	}
	var out []Interval
	add := func(start time.Time) {
		end := start.Add(w.Duration)
		if !end.After(from) || !start.Before(to) {
			return
		}
		iv := Interval{maxTime(start, from), minTime(end, to)}
		if n := len(out); n > 0 && !iv.From.After(out[n-1].To) {
			out[n-1].To = maxTime(out[n-1].To, iv.To)
			return
		}
		out = append(out, iv)
	}
	if w.Recurrence != Daily && w.Recurrence != Weekly {
		add(w.Start)
		return out
	}
	first := w.Start.In(loc)
	// The first local day whose occurrence could still reach from.
	day := midnight(maxTime(first, from.Add(-w.Duration)).In(loc))
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		start := w.on(day, first)
		if start.Before(w.Start) {
			continue
		}
		if w.Recurrence == Weekly && w.Weekdays&(1<<uint(day.Weekday())) == 0 {
			continue
		}
		add(start)
	}
	return out
}

// on is the occurrence on the given local day. The first day's is Start
// itself. On the other days a time that does not exist (the hour skipped
// in spring) is moved forward by the gap, and one that exists twice (the
// hour repeated in autumn) is the first of the two.
func (w Window) on(day, first time.Time) time.Time {
	if day.Year() == first.Year() && day.YearDay() == first.YearDay() {
		return w.Start
	}
	t := time.Date(day.Year(), day.Month(), day.Day(), first.Hour(), first.Minute(), first.Second(), 0, day.Location())
	// time.Date does not promise which of two equal wall-clock times it
	// returns; the earlier one shows the same clock under the larger
	// offset of a few hours before.
	_, now := t.Zone()
	if _, before := t.Add(-3 * time.Hour).Zone(); before > now {
		if e := t.Add(-time.Duration(before-now) * time.Second); e.Hour() == t.Hour() && e.Minute() == t.Minute() {
			return e
		}
	}
	return t
}

// Active reports whether the window is in effect at t.
func (w Window) Active(t time.Time, loc *time.Location) bool {
	return len(w.Occurrences(t, t.Add(time.Second), loc)) > 0
}

// Next returns the occurrence in effect at t or, if there is none, the
// next one to begin. ok is false when the window has no occurrence ending
// after t: a one-time window that is over, or a weekly one without days.
func (w Window) Next(t time.Time, loc *time.Location) (iv Interval, ok bool) {
	if w.Recurrence != Daily && w.Recurrence != Weekly {
		iv = Interval{w.Start, w.Start.Add(w.Duration)}
		return iv, iv.To.After(t)
	}
	// An occurrence in effect at t began at most Duration before it; a
	// recurring one begins within a week of t or of the window's start.
	from := maxTime(t, w.Start).Add(-w.Duration)
	for _, iv := range w.Occurrences(from, maxTime(t, w.Start).AddDate(0, 0, 8), loc) {
		if iv.To.After(t) {
			return iv, true
		}
	}
	return Interval{}, false
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
