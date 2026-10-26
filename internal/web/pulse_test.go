package web

import (
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// pulseOf turns the 96 quarter-hour buckets of LatencyHistory into the
// paths of a Heartline pulse (docs/04 "Response pulse"): a line through
// the buckets' averages at their centres, the area under it, a dot at the
// line's end, gaps where a bucket has no sample, the dashed floor since
// the service went down, and the summary in words.
func TestPulseOf(t *testing.T) {
	from := time.Date(2026, 10, 25, 14, 10, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	at := func(i int) time.Time { return from.Add(time.Duration(i) * 15 * time.Minute) }
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	stats := store.LatencyStats{Samples: 30, Min: ms(40), Max: ms(80)}

	// Three buckets in a row: the tallest sits under the top with room to
	// spare (the scale is 1.15 × the highest average), the floor is y 62.
	p := pulseOf([]store.LatencyPoint{{At: at(0), Checks: 10, Samples: 10, Avg: ms(40)}, {At: at(1), Checks: 10, Samples: 10, Avg: ms(80)},
		{At: at(2), Checks: 10, Samples: 10, Avg: ms(60)}}, stats, from, to, time.Time{})
	want := templates.Pulse{Line: "M5 35.9L15 9.8L25 22.9", Area: "M5 62L5 35.9L15 9.8L25 22.9L25 62Z", End: "M25 22.9h0",
		Text: "Response over 24 hours: min 40 ms, median 60 ms, max 80 ms"}
	if p != want {
		t.Errorf("pulse\n got %+v\nwant %+v", p, want)
	}

	// A missing bucket and one with failures only break the line and the
	// area; the median is the middle bucket average.
	p = pulseOf([]store.LatencyPoint{{At: at(0), Checks: 10, Samples: 10, Avg: ms(40)}, {At: at(1), Checks: 10, Samples: 10, Avg: ms(80)},
		{At: at(3), Checks: 10, Failures: 10}, {At: at(95), Checks: 10, Samples: 10, Avg: ms(60)}}, stats, from, to, time.Time{})
	want = templates.Pulse{Line: "M5 35.9L15 9.8M955 22.9", Area: "M5 62L5 35.9L15 9.8L15 62ZM955 62L955 22.9L955 62Z", End: "M955 22.9h0",
		Text: "Response over 24 hours: min 40 ms, median 60 ms, max 80 ms"}
	if p != want {
		t.Errorf("pulse with gaps\n got %+v\nwant %+v", p, want)
	}

	// Down for 18 minutes: the floor is dashed from that moment to now and
	// the dot sits at its end.
	p = pulseOf([]store.LatencyPoint{{At: at(0), Checks: 10, Samples: 10, Avg: ms(40)}}, stats, from, to, to.Add(-18*time.Minute))
	want = templates.Pulse{Line: "M5 9.8", Area: "M5 62L5 9.8L5 62Z", Tail: "M948 62H960", End: "M960 62h0",
		Text: "Response over 24 hours: min 40 ms, median 40 ms, max 80 ms"}
	if p != want {
		t.Errorf("pulse while down\n got %+v\nwant %+v", p, want)
	}

	// No samples at all: no line, no dot, and the summary says so. Down for
	// days, the whole floor is dashed.
	p = pulseOf(nil, store.LatencyStats{}, from, to, time.Time{})
	if want := (templates.Pulse{Text: "No response data in the last 24 hours."}); p != want {
		t.Errorf("empty pulse\n got %+v\nwant %+v", p, want)
	}
	p = pulseOf(nil, store.LatencyStats{Checks: 5, Failures: 5}, from, to, from.AddDate(0, 0, -2))
	if want := (templates.Pulse{Tail: "M0 62H960", End: "M960 62h0", Text: "No response data in the last 24 hours."}); p != want {
		t.Errorf("empty pulse while down\n got %+v\nwant %+v", p, want)
	}
}

// medianLatency is the page's "Response, median" figure: the median of
// the last response times of the services that show one and have
// replied; "" without any.
func TestMedianLatency(t *testing.T) {
	row := func(state string, ms int64) templates.PublicRow {
		return templates.PublicRow{State: state, LatencyMS: ms, HasLatency: ms > 0}
	}
	for _, tc := range []struct {
		rows []templates.PublicRow
		want string
	}{
		{nil, ""},
		{[]templates.PublicRow{row(templates.StateUp, 0)}, ""},
		{[]templates.PublicRow{row(templates.StateUp, 71)}, "71 ms"},
		{[]templates.PublicRow{row(templates.StateUp, 276), row(templates.StateUp, 46), row(templates.StateUp, 71)}, "71 ms"},
		{[]templates.PublicRow{row(templates.StateUp, 46), row(templates.StateUp, 72)}, "59 ms"},
		// A down service has not replied; a paused one is not asked.
		{[]templates.PublicRow{row(templates.StateUp, 46), row(templates.StateDown, 5000), row(templates.StatePaused, 900)}, "46 ms"},
		{[]templates.PublicRow{row(templates.StateDown, 5000)}, ""},
	} {
		groups := []templates.PublicGroup{{Rows: tc.rows}}
		if got := medianLatency(groups); got != tc.want {
			t.Errorf("medianLatency(%v) = %q, want %q", tc.rows, got, tc.want)
		}
	}
}
