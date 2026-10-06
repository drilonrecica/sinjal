package templates

import "strconv"

// HistoryView is the History tab: a summary in words, the latency figures,
// the latency chart and the availability timeline over one range.
type HistoryView struct {
	Range      string // "the last 24 hours"
	RangeError string // why a requested custom range was not used
	HasData    bool   // there are checks in the range
	Summary    string // everything below in one or two sentences
	Stats      []Fact
	Uptime     string // raw, "99.95%"; "—" without data
	Adjusted   string
	Series     string // JSON for chart.js: {"t":[…],"avg":[…],"max":[…],"fail":[…]}
	Overlays   string // JSON: range, outage, maintenance and paused spans, incident starts
	Timeline   []TimelineSegment
	From, To   string // the timeline's ends, local time
	Zone       string // the instance time zone, for the chart's time axis

	// The range selector, on the History tab only.
	Selector   bool
	MonitorID  string
	Presets    []RangeOption
	FromValue  string // datetime-local values of the shown range
	ToValue    string
	MaxValue   string // now: a range starts in the past
	CustomOpen bool   // a custom range is shown, so its form starts open
}

// RangeOption is one preset of the range selector, a plain link.
type RangeOption struct {
	Label   string
	Href    string
	Current bool
}

// TimelineSegment is one stretch of the availability timeline, in a
// viewBox 1000 wide.
type TimelineSegment struct {
	X, W  float64
	Class string // up, down, maintenance, paused, none
	Title string // "Down: 6 Oct 12:03 to 6 Oct 12:10 (7m)"
}

// Sparkline is recent latency drawn in a viewBox of 100 by 24.
type Sparkline struct {
	Points   string    // polyline points of the successful checks
	Failures []float64 // x of each failed check
	Label    string    // the same in words
}

func coord(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// timelineLegend lists the timeline's states with their text.
var timelineLegend = []struct{ Class, Label string }{
	{"up", "Up"}, {"down", "Down"}, {"maintenance", "Maintenance"}, {"paused", "Paused"}, {"none", "No data"},
}
