// Package history turns a requested history range into times
// (docs/01_PRODUCT.md "History"): a preset or a custom range in the
// instance time zone. The queries over the range are in internal/store.
package history

import (
	"net/url"
	"time"
)

// Preset is a named range ending now.
type Preset struct {
	Key   string // the ?range= value
	Label string
	// Length is a fixed duration, or zero for a calendar span (Days).
	Length time.Duration
	Days   int // calendar days, kept in the instance time zone
}

// Presets are offered in this order. Ranges of days are calendar days, so
// "7 days" ends at the same local time a week ago across DST changes.
var Presets = []Preset{
	{Key: "1h", Label: "1 hour", Length: time.Hour},
	{Key: "24h", Label: "24 hours", Length: 24 * time.Hour},
	{Key: "7d", Label: "7 days", Days: 7},
	{Key: "30d", Label: "30 days", Days: 30},
	{Key: "90d", Label: "90 days", Days: 90},
	{Key: "1y", Label: "1 year", Days: 365},
}

// DefaultPreset is the range shown when none is asked for.
const DefaultPreset = "24h"

// MaxCustom bounds a custom range.
const MaxCustom = 400 * 24 * time.Hour

// datetimeLocal is the value format of <input type="datetime-local">.
const datetimeLocal = "2006-01-02T15:04"

// Range is a resolved history range [From, To).
type Range struct {
	From, To time.Time
	Preset   string // the preset's key, or "" for a custom range
	// Error explains why a requested custom range was not used; the
	// default preset is shown instead.
	Error string
}

// Custom reports whether the range is a custom one.
func (r Range) Custom() bool { return r.Preset == "" }

// Parse reads ?range=<preset> or ?from=…&to=… (datetime-local, read in
// loc). A custom range ending in the future ends now. Anything unknown or
// invalid gives the default preset; an invalid custom range also says why.
func Parse(q url.Values, now time.Time, loc *time.Location) Range {
	if q.Get("from") != "" || q.Get("to") != "" {
		r, msg := custom(q.Get("from"), q.Get("to"), now, loc)
		if msg == "" {
			return r
		}
		r = preset(DefaultPreset, now, loc)
		r.Error = msg
		return r
	}
	return preset(q.Get("range"), now, loc)
}

func preset(key string, now time.Time, loc *time.Location) Range {
	p := Presets[1]
	for _, c := range Presets {
		if c.Key == key {
			p = c
		}
	}
	from := now.Add(-p.Length)
	if p.Days > 0 {
		from = now.In(loc).AddDate(0, 0, -p.Days)
	}
	return Range{From: from, To: now, Preset: p.Key}
}

func custom(fromText, toText string, now time.Time, loc *time.Location) (Range, string) {
	from, err1 := time.ParseInLocation(datetimeLocal, fromText, loc)
	to, err2 := time.ParseInLocation(datetimeLocal, toText, loc)
	switch {
	case err1 != nil || err2 != nil:
		return Range{}, "Enter a start and an end date and time."
	case !to.After(from):
		return Range{}, "The end must be after the start."
	case !from.Before(now):
		return Range{}, "The start must be in the past."
	}
	if to.After(now) {
		to = now
	}
	if to.Sub(from) > MaxCustom {
		return Range{}, "Choose a range of at most 400 days."
	}
	return Range{From: from, To: to}, ""
}
