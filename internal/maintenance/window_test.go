package maintenance

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// local is a wall-clock time in loc, "2006-01-02 15:04".
func local(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// show renders intervals as local "from–to" times with their UTC offsets.
func show(ivs []Interval, loc *time.Location) string {
	var parts []string
	for _, iv := range ivs {
		parts = append(parts, iv.From.In(loc).Format("01-02 15:04-0700")+"→"+iv.To.In(loc).Format("01-02 15:04-0700"))
	}
	return strings.Join(parts, " ")
}

const (
	sun = 1 << time.Sunday
	mon = 1 << time.Monday
	wed = 1 << time.Wednesday
	sat = 1 << time.Saturday
)

func TestOccurrences(t *testing.T) {
	bel := zone(t, "Europe/Belgrade")
	ny := zone(t, "America/New_York")
	cases := []struct {
		name     string
		loc      *time.Location
		w        Window
		from, to string
		want     string
	}{
		{"one-time inside", bel, Window{Start: local(t, bel, "2026-06-10 02:00"), Duration: time.Hour},
			"2026-06-10 00:00", "2026-06-11 00:00", "06-10 02:00+0200→06-10 03:00+0200"},
		{"one-time clipped at both ends", bel, Window{Start: local(t, bel, "2026-06-10 02:00"), Duration: 3 * time.Hour},
			"2026-06-10 03:00", "2026-06-10 04:00", "06-10 03:00+0200→06-10 04:00+0200"},
		{"one-time outside", bel, Window{Start: local(t, bel, "2026-06-10 02:00"), Duration: time.Hour},
			"2026-06-10 03:00", "2026-06-11 00:00", ""},
		{"daily, nothing before the start", bel, Window{Start: local(t, bel, "2026-06-10 02:00"), Duration: time.Hour, Recurrence: Daily},
			"2026-06-08 00:00", "2026-06-12 00:00", "06-10 02:00+0200→06-10 03:00+0200 06-11 02:00+0200→06-11 03:00+0200"},
		{"daily across midnight, in progress at the range start", bel, Window{Start: local(t, bel, "2026-06-10 23:00"), Duration: 2 * time.Hour, Recurrence: Daily},
			"2026-06-12 00:30", "2026-06-13 00:00", "06-12 00:30+0200→06-12 01:00+0200 06-12 23:00+0200→06-13 00:00+0200"},
		{"daily, longer than a day, merged", bel, Window{Start: local(t, bel, "2026-06-10 00:00"), Duration: 30 * time.Hour, Recurrence: Daily},
			"2026-06-10 00:00", "2026-06-13 00:00", "06-10 00:00+0200→06-13 00:00+0200"},
		{"weekly mask", bel, Window{Start: local(t, bel, "2026-06-01 22:00"), Duration: time.Hour, Recurrence: Weekly, Weekdays: mon | wed},
			"2026-06-01 00:00", "2026-06-11 00:00", "06-01 22:00+0200→06-01 23:00+0200 06-03 22:00+0200→06-03 23:00+0200 06-08 22:00+0200→06-08 23:00+0200 06-10 22:00+0200→06-10 23:00+0200"},
		{"weekly, the start's own day not in the mask", bel, Window{Start: local(t, bel, "2026-06-01 22:00"), Duration: time.Hour, Recurrence: Weekly, Weekdays: sat},
			"2026-06-01 00:00", "2026-06-08 00:00", "06-06 22:00+0200→06-06 23:00+0200"},
		{"weekly without days", bel, Window{Start: local(t, bel, "2026-06-01 22:00"), Duration: time.Hour, Recurrence: Weekly},
			"2026-06-01 00:00", "2026-06-30 00:00", ""},
		// Europe/Belgrade: 29 March 2026 02:00 → 03:00, 25 October 2026 03:00 → 02:00.
		{"daily keeps the local time across spring DST", bel, Window{Start: local(t, bel, "2026-03-27 04:00"), Duration: time.Hour, Recurrence: Daily},
			"2026-03-28 00:00", "2026-03-31 00:00", "03-28 04:00+0100→03-28 05:00+0100 03-29 04:00+0200→03-29 05:00+0200 03-30 04:00+0200→03-30 05:00+0200"},
		{"daily keeps the local time across autumn DST", bel, Window{Start: local(t, bel, "2026-10-23 04:00"), Duration: time.Hour, Recurrence: Daily},
			"2026-10-24 00:00", "2026-10-27 00:00", "10-24 04:00+0200→10-24 05:00+0200 10-25 04:00+0100→10-25 05:00+0100 10-26 04:00+0100→10-26 05:00+0100"},
		{"a time skipped in spring moves forward", bel, Window{Start: local(t, bel, "2026-03-27 02:30"), Duration: time.Hour, Recurrence: Daily},
			"2026-03-29 00:00", "2026-03-30 00:00", "03-29 03:30+0200→03-29 04:30+0200"},
		{"a time repeated in autumn occurs once", bel, Window{Start: local(t, bel, "2026-10-23 02:30"), Duration: 15 * time.Minute, Recurrence: Daily},
			"2026-10-25 00:00", "2026-10-26 00:00", "10-25 02:30+0200→10-25 02:45+0200"},
		{"a 2 h window over the spring change lasts 2 h", bel, Window{Start: local(t, bel, "2026-03-28 01:00"), Duration: 2 * time.Hour, Recurrence: Daily},
			"2026-03-29 00:00", "2026-03-30 00:00", "03-29 01:00+0100→03-29 04:00+0200"},
		{"a 2 h window over the autumn change lasts 2 h", bel, Window{Start: local(t, bel, "2026-10-24 01:00"), Duration: 2 * time.Hour, Recurrence: Daily},
			"2026-10-25 00:00", "2026-10-26 00:00", "10-25 01:00+0200→10-25 02:00+0100"},
		// America/New_York: 8 March 2026 02:00 → 03:00, 1 November 2026 02:00 → 01:00.
		{"weekly on Sunday across spring DST", ny, Window{Start: local(t, ny, "2026-03-01 06:00"), Duration: time.Hour, Recurrence: Weekly, Weekdays: sun},
			"2026-03-01 00:00", "2026-03-09 00:00", "03-01 06:00-0500→03-01 07:00-0500 03-08 06:00-0400→03-08 07:00-0400"},
		{"weekly on Sunday across autumn DST", ny, Window{Start: local(t, ny, "2026-10-25 06:00"), Duration: time.Hour, Recurrence: Weekly, Weekdays: sun},
			"2026-10-25 00:00", "2026-11-02 00:00", "10-25 06:00-0400→10-25 07:00-0400 11-01 06:00-0500→11-01 07:00-0500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := show(c.w.Occurrences(local(t, c.loc, c.from), local(t, c.loc, c.to), c.loc), c.loc)
			if got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// Every occurrence of a window that keeps 02:00 in Belgrade over a year
// starts at 02:00 local time and lasts its duration.
func TestDailyKeepsItsTimeOfDayAllYear(t *testing.T) {
	bel := zone(t, "Europe/Belgrade")
	w := Window{Start: local(t, bel, "2026-01-01 02:00"), Duration: 30 * time.Minute, Recurrence: Daily}
	ivs := w.Occurrences(local(t, bel, "2026-01-01 00:00"), local(t, bel, "2027-01-01 00:00"), bel)
	if len(ivs) != 365 {
		t.Fatalf("%d occurrences, want 365", len(ivs))
	}
	for _, iv := range ivs {
		from := iv.From.In(bel)
		// 29 March has no 02:00; that day's occurrence is at 03:00.
		want := 2
		if from.Month() == time.March && from.Day() == 29 {
			want = 3
		}
		if from.Hour() != want || from.Minute() != 0 || iv.To.Sub(iv.From) != 30*time.Minute {
			t.Fatalf("occurrence %s", show([]Interval{iv}, bel))
		}
	}
}

func TestActiveAndNext(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time { return local(t, loc, s) }
	daily := Window{Start: at("2026-06-10 02:00"), Duration: time.Hour, Recurrence: Daily}
	once := Window{Start: at("2027-06-10 02:00"), Duration: time.Hour}
	for _, c := range []struct {
		w      Window
		t      string
		active bool
		next   string // "" for none
	}{
		{daily, "2026-06-09 12:00", false, "06-10 02:00+0000→06-10 03:00+0000"},
		{daily, "2026-06-10 02:00", true, "06-10 02:00+0000→06-10 03:00+0000"},
		{daily, "2026-06-10 02:59", true, "06-10 02:00+0000→06-10 03:00+0000"},
		{daily, "2026-06-10 03:00", false, "06-11 02:00+0000→06-11 03:00+0000"},
		{once, "2026-06-10 03:00", false, "06-10 02:00+0000→06-10 03:00+0000"},
		{once, "2027-06-10 03:00", false, ""},
		{Window{Start: at("2026-06-01 02:00"), Duration: time.Hour, Recurrence: Weekly}, "2026-06-10 00:00", false, ""},
	} {
		name := fmt.Sprintf("%s %s at %s", c.w.Recurrence, c.w.Start.Format(time.DateOnly), c.t)
		if got := c.w.Active(at(c.t), loc); got != c.active {
			t.Errorf("%s: Active = %v", name, got)
		}
		iv, ok := c.w.Next(at(c.t), loc)
		got := ""
		if ok {
			got = show([]Interval{iv}, loc)
		}
		if got != c.next {
			t.Errorf("%s: Next = %q, want %q", name, got, c.next)
		}
	}
}

func TestScope(t *testing.T) {
	all := Scope{}
	some := Scope{Monitors: []string{"m1"}, Tags: []string{"Prod"}}
	for _, c := range []struct {
		s    Scope
		id   string
		tags []string
		want bool
	}{
		{all, "m9", nil, true},
		{some, "m1", nil, true},
		{some, "m2", []string{"edge", "prod"}, true},
		{some, "m2", []string{"edge"}, false},
		{some, "m2", nil, false},
	} {
		if got := c.s.Covers(c.id, c.tags); got != c.want {
			t.Errorf("%+v covers %s %v = %v", c.s, c.id, c.tags, got)
		}
	}
}

func TestLocal(t *testing.T) {
	bel := zone(t, "Europe/Belgrade")
	for _, c := range []struct {
		h, m int
		day  int
		want string
	}{
		{2, 30, 25, "2026-10-25T00:30:00Z"}, // repeated: the first, still CEST
		{3, 30, 25, "2026-10-25T02:30:00Z"},
		{2, 30, 24, "2026-10-24T00:30:00Z"},
	} {
		if got := Local(2026, time.October, c.day, c.h, c.m, 0, bel).UTC().Format(time.RFC3339); got != c.want {
			t.Errorf("%d %02d:%02d = %s, want %s", c.day, c.h, c.m, got, c.want)
		}
	}
	if got := Local(2026, time.March, 29, 2, 30, 0, bel).UTC().Format(time.RFC3339); got != "2026-03-29T01:30:00Z" {
		t.Errorf("skipped 02:30 = %s, want 03:30 CEST", got)
	}
}
