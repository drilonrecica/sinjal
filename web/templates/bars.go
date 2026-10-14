package templates

import "strconv"

// Beat results; a slot without a check has none.
const (
	BeatUp   = "up"
	BeatDown = "down"
)

// Beat is one check in a beat bar.
type Beat struct {
	Result string // BeatUp, BeatDown, or "" for an empty slot
	Tip    string // time, duration and result, e.g. "14:02:31 · 128 ms · Up"; "" in a list row
}

// BeatBarView is a monitor's recent checks (docs/04 "beat bar"). The web
// package builds it from the engine's memory (internal/recent).
type BeatBarView struct {
	Beats     []Beat // newest first, padded with empty slots to the bar's length
	Newest    int64  // Unix seconds of the newest check, 0 without; beats.js slides in what is newer
	Label     string // the bar in words, for assistive technology
	Focusable bool   // the detail bar: keyboard users move through its beats
	// TipsHref is where a list row's bar fetches its tips on the first
	// hover; its beats carry none. "" for the detail bar and without checks.
	TipsHref string
}

func (b BeatBarView) newest() string { return strconv.FormatInt(b.Newest, 10) }

// Day classes of a 90-day bar.
const (
	DayUp          = "up"      // 100 % adjusted
	DayPartial     = "partial" // some downtime, at least 95 %
	DayDown        = "down"    // below 95 %
	DayMaintenance = "maint"   // fully covered by excluded maintenance
	DayNoData      = "none"    // before the monitor existed, or paused all day
)

// Day is one bar of a 90-day strip.
type Day struct {
	Class string // Day…
	Label string // date, uptime and incidents, in words
}
