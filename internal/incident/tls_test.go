package incident

import (
	"slices"
	"testing"
	"time"
)

func TestCrossedThresholds(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	days := []int{30, 14, 7}
	cases := []struct {
		left time.Duration
		want []int
	}{
		{31 * day, nil},
		{31*day - time.Second, []int{30}},
		{30 * day, []int{30}},
		{29 * day, []int{30}},
		{15 * day, []int{30}},
		{14*day + time.Hour, []int{30, 14}},
		{7 * day, []int{30, 14, 7}},
		{time.Hour, []int{30, 14, 7}},
		{-time.Hour, []int{30, 14, 7}},
	}
	for _, c := range cases {
		if got := CrossedThresholds(days, at.Add(c.left), at); !slices.Equal(got, c.want) {
			t.Errorf("%v left: %v, want %v", c.left, got, c.want)
		}
	}
	if got := CrossedThresholds([]int{7, 30}, at.Add(20*day), at); !slices.Equal(got, []int{30}) {
		t.Errorf("unsorted thresholds: %v", got)
	}
	if got := CrossedThresholds(nil, at, at); got != nil {
		t.Errorf("no thresholds: %v", got)
	}
	for left, want := range map[time.Duration]int{14*day + time.Hour: 14, 14 * day: 14, 14*day - time.Second: 13, time.Second: 0, -time.Second: -1} {
		if got := DaysLeft(at.Add(left), at); got != want {
			t.Errorf("DaysLeft(%v) = %d, want %d", left, got, want)
		}
	}
}
