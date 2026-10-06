package incident

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// m is t0 plus n minutes.
func m(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

func span(a, b int) Span { return Span{m(a), m(b)} }

func TestUptime(t *testing.T) {
	day := span(0, 1440)
	cases := []struct {
		name          string
		in            UptimeInput
		raw, adjusted string
	}{
		{"nothing happened", UptimeInput{Range: day, Created: m(-10), Now: m(2000)}, "100.00%", "100.00%"},
		{"one hour down", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Down: []Span{span(60, 120)}}, "95.83%", "95.83%"},
		{"active incident runs to now", UptimeInput{Range: day, Created: m(-10), Now: m(1440), Down: []Span{{From: m(1380)}}}, "95.83%", "95.83%"},
		{"range clipped to now", UptimeInput{Range: day, Created: m(-10), Now: m(720), Down: []Span{span(660, 720)}}, "91.66%", "91.66%"},
		{"range clipped to creation", UptimeInput{Range: day, Created: m(720), Now: m(2000), Down: []Span{span(660, 780)}}, "91.66%", "91.66%"},
		{"incidents outside the range", UptimeInput{Range: day, Created: m(-100), Now: m(2000), Down: []Span{span(-50, 0), span(1440, 1500)}}, "100.00%", "100.00%"},
		{"overlapping incidents count once", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Down: []Span{span(60, 120), span(90, 150)}}, "93.75%", "93.75%"},
		{"paused time is not observed", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Paused: []Span{span(0, 720)}, Down: []Span{span(780, 840)}}, "91.66%", "91.66%"},
		{"downtime while paused does not count", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Paused: []Span{span(0, 720)}, Down: []Span{span(660, 780)}}, "91.66%", "91.66%"},
		{"open pause runs to now", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Paused: []Span{{From: m(720)}}, Down: []Span{span(0, 72)}}, "90.00%", "90.00%"},
		{"fully paused: no data", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Paused: []Span{span(-10, 1500)}}, "—", "—"},
		{"range before creation: no data", UptimeInput{Range: day, Created: m(2000), Now: m(3000)}, "—", "—"},
		{"maintenance during an outage", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Down: []Span{span(60, 120)}, Excluded: []Span{span(60, 120)}}, "95.83%", "100.00%"},
		{"half an outage in maintenance", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Down: []Span{span(60, 180)}, Excluded: []Span{span(120, 240)}}, "91.66%", "95.45%"},
		{"maintenance while paused excludes nothing more", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Paused: []Span{span(0, 720)}, Down: []Span{span(780, 840)}, Excluded: []Span{span(600, 720)}}, "91.66%", "91.66%"},
		{"all of it maintenance", UptimeInput{Range: day, Created: m(-10), Now: m(2000), Down: []Span{span(60, 120)}, Excluded: []Span{span(-10, 1500)}}, "95.83%", "—"},
		{"one second down in a year", UptimeInput{Range: Span{t0, t0.AddDate(1, 0, 0)}, Created: m(-10), Now: t0.AddDate(2, 0, 0), Down: []Span{{m(1), m(1).Add(time.Second)}}}, "99.99%", "99.99%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, adj := Uptime(c.in)
			if raw.Percent() != c.raw || adj.Percent() != c.adjusted {
				t.Errorf("raw %s (%d/%d), adjusted %s (%d/%d); want %s, %s",
					raw.Percent(), raw.Up, raw.Den, adj.Percent(), adj.Up, adj.Den, c.raw, c.adjusted)
			}
		})
	}
}

func TestRatioPercentTruncates(t *testing.T) {
	for _, c := range []struct {
		r    Ratio
		want string
	}{
		{Ratio{99999, 100000}, "99.99%"},
		{Ratio{100000, 100000}, "100.00%"},
		{Ratio{9999999, 10000000}, "99.99%"},
		{Ratio{0, 5}, "0.00%"},
		{Ratio{1, 3}, "33.33%"},
		{Ratio{2, 3}, "66.66%"},
		{Ratio{}, "—"},
	} {
		if got := c.r.Percent(); got != c.want {
			t.Errorf("%d/%d = %s, want %s", c.r.Up, c.r.Den, got, c.want)
		}
	}
	if (Ratio{}).OK() || !(Ratio{0, 1}).OK() {
		t.Error("OK is wrong")
	}
}

// Sub-second times are taken as stored, in whole seconds.
func TestUptimeWholeSeconds(t *testing.T) {
	raw, _ := Uptime(UptimeInput{Range: Span{t0, t0.Add(100 * time.Second)}, Created: t0.Add(-time.Hour), Now: t0.Add(time.Hour),
		Down: []Span{{t0.Add(10*time.Second + 900*time.Millisecond), t0.Add(20*time.Second + 100*time.Millisecond)}}})
	if raw.Up != 90 || raw.Den != 100 {
		t.Fatalf("raw = %d/%d, want 90/100", raw.Up, raw.Den)
	}
}
