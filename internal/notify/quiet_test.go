package notify

import (
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "07:05": 425, "23:59": 1439, "12:30": 750} {
		if got, ok := ParseClock(in); !ok || got != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "7:05", "24:00", "12:60", "12-30", "ab:cd", "12:3", "12:300", " 2:30", "+1:30"} {
		if _, ok := ParseClock(in); ok {
			t.Errorf("ParseClock(%q) accepted", in)
		}
	}
}

func TestInQuietHours(t *testing.T) {
	belgrade, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	utc := func(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name       string
		start, end string
		at         time.Time
		loc        *time.Location
		want       bool
	}{
		{"inside a day window", "09:00", "17:00", utc(12, 0), time.UTC, true},
		{"start is in", "09:00", "17:00", utc(9, 0), time.UTC, true},
		{"end is out", "09:00", "17:00", utc(17, 0), time.UTC, false},
		{"the minute before the end", "09:00", "17:00", utc(16, 59), time.UTC, true},
		{"before a day window", "09:00", "17:00", utc(8, 59), time.UTC, false},
		{"across midnight, late evening", "23:00", "07:00", utc(23, 30), time.UTC, true},
		{"across midnight, early morning", "23:00", "07:00", utc(6, 59), time.UTC, true},
		{"across midnight, start is in", "23:00", "07:00", utc(23, 0), time.UTC, true},
		{"across midnight, end is out", "23:00", "07:00", utc(7, 0), time.UTC, false},
		{"across midnight, midday", "23:00", "07:00", utc(12, 0), time.UTC, false},
		{"across midnight, the minute before", "23:00", "07:00", utc(22, 59), time.UTC, false},
		{"equal bounds are no window", "08:00", "08:00", utc(8, 0), time.UTC, false},
		{"unreadable start", "8", "09:00", utc(8, 30), time.UTC, false},
		{"unreadable end", "08:00", "", utc(8, 30), time.UTC, false},
		{"nil zone is UTC", "09:00", "17:00", utc(12, 0), nil, true},
		// 21:30 UTC on 6 October is 23:30 in Belgrade (CEST, +2).
		{"read in the instance zone", "23:00", "07:00", utc(21, 30), belgrade, true},
		{"not in UTC terms", "23:00", "07:00", utc(21, 30), time.UTC, false},
		// The night clocks go back (25 October 2026): 02:30 local happens
		// twice, both inside; 05:30 UTC is 06:30 CET, still inside, and
		// 06:00 UTC is 07:00 CET, out. In summer 05:00 UTC is 07:00.
		{"repeated hour, first", "23:00", "07:00", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), belgrade, true},
		{"repeated hour, second", "23:00", "07:00", time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), belgrade, true},
		{"winter time end", "23:00", "07:00", time.Date(2026, 10, 25, 5, 30, 0, 0, time.UTC), belgrade, true},
		{"winter time after the end", "23:00", "07:00", time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC), belgrade, false},
		{"summer time after the end", "23:00", "07:00", time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC), belgrade, false},
	}
	for _, c := range cases {
		if got := InQuietHours(c.start, c.end, c.at, c.loc); got != c.want {
			t.Errorf("%s: InQuietHours(%s, %s, %s) = %v, want %v", c.name, c.start, c.end, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}
