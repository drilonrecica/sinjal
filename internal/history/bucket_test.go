package history

import (
	"testing"
	"time"
)

func ok(ms ...float64) []Sample {
	out := make([]Sample, len(ms))
	for i, v := range ms {
		out[i] = Sample{OK: true, MS: v}
	}
	return out
}

func seq(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}

func TestFloor(t *testing.T) {
	bel, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 29, 3, 17, 42, 0, bel) // the day CEST starts: 01:17:42 UTC
	cases := []struct {
		res  time.Duration
		want string
	}{
		{Res5m, "2026-03-29T01:15:00Z"},
		{Res1h, "2026-03-29T01:00:00Z"},
		{Res1d, "2026-03-29T00:00:00Z"}, // a UTC day, whatever the zone
	}
	for _, c := range cases {
		got := Floor(at, c.res)
		if got.Format(time.RFC3339) != c.want || got.Location() != time.UTC {
			t.Errorf("Floor(%v) = %v, want %s in UTC", c.res, got, c.want)
		}
	}
}

func TestNearestRank(t *testing.T) {
	// rank ceil(0.95 n) of 1..n is the value itself.
	for n, want := range map[int]float64{1: 1, 2: 2, 19: 19, 20: 19, 21: 20, 100: 95, 101: 96} {
		if got := NearestRank(seq(n)); got != want {
			t.Errorf("n=%d: p95 = %v, want %v", n, got, want)
		}
	}
}

func TestFromRaw(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 5, 0, 0, time.UTC)
	samples := append(ok(40, 10, 30, 20), Sample{OK: false, MS: 5000}, Sample{OK: false, MS: 9000})
	b := FromRaw(start, samples)
	want := Bucket{Start: start, Total: 6, Success: 4, Failure: 2, Min: 10, Max: 40, Avg: 25, P95: 40}
	if b != want {
		t.Errorf("FromRaw = %+v, want %+v", b, want)
	}

	// A failure's duration is not latency, so a bucket of failures has none.
	b = FromRaw(start, []Sample{{OK: false, MS: 30000}})
	if want := (Bucket{Start: start, Total: 1, Failure: 1}); b != want {
		t.Errorf("failures only = %+v, want %+v", b, want)
	}
	if b := FromRaw(start, nil); b != (Bucket{Start: start}) {
		t.Errorf("empty = %+v", b)
	}

	// The p95 is exact: 100 samples 1..100 give the 95th.
	if b := FromRaw(start, ok(seq(100)...)); b.P95 != 95 || b.Avg != 50.5 {
		t.Errorf("1..100: p95 %v avg %v, want 95 and 50.5", b.P95, b.Avg)
	}
}

func TestMerge(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a := Bucket{Total: 10, Success: 8, Failure: 2, Min: 20, Max: 300, Avg: 50, P95: 200}
	b := Bucket{Total: 4, Success: 2, Failure: 2, Min: 10, Max: 90, Avg: 80, P95: 90}
	fails := Bucket{Total: 3, Failure: 3}
	got := Merge(start, []Bucket{a, fails, b})
	// avg (8×50 + 2×80) / 10 = 56; p95: weights 2 (90) and 8 (200), rank 10 → 200.
	want := Bucket{Start: start, Total: 17, Success: 10, Failure: 7, Min: 10, Max: 300, Avg: 56, P95: 200}
	if got != want {
		t.Errorf("Merge = %+v, want %+v", got, want)
	}

	// A bucket of failures only lends no latency, even as the first child.
	got = Merge(start, []Bucket{fails, b})
	if got.Min != 10 || got.Max != 90 || got.Avg != 80 || got.P95 != 90 || got.Total != 7 {
		t.Errorf("failures first = %+v", got)
	}
	if got := Merge(start, []Bucket{fails}); got != (Bucket{Start: start, Total: 3, Failure: 3}) {
		t.Errorf("failures only = %+v", got)
	}
	if got := Merge(start, nil); got != (Bucket{Start: start}) {
		t.Errorf("empty = %+v", got)
	}

	// One child comes back as itself, at the new start.
	one := a
	one.Start = start
	if got := Merge(start, []Bucket{a}); got != one {
		t.Errorf("one child = %+v, want %+v", got, one)
	}
}

func TestMergeOfMergesKeepsCounts(t *testing.T) {
	// Rolling 5m into 1h into 1d keeps every count, the extremes and the
	// weighted average whatever the grouping.
	var fives []Bucket
	for i := range 288 {
		var s []Sample
		for j := range 10 {
			s = append(s, Sample{OK: (i+j)%7 != 0, MS: float64(10 + (i*j)%90)})
		}
		fives = append(fives, FromRaw(time.Time{}, s))
	}
	var hours []Bucket
	for h := range 24 {
		hours = append(hours, Merge(time.Time{}, fives[h*12:h*12+12]))
	}
	day, flat := Merge(time.Time{}, hours), Merge(time.Time{}, fives)
	if day.Total != 2880 || day.Total != day.Success+day.Failure {
		t.Fatalf("day counts %+v", day)
	}
	if day.Success != flat.Success || day.Min != flat.Min || day.Max != flat.Max {
		t.Errorf("day %+v differs from flat %+v", day, flat)
	}
	if d := day.Avg - flat.Avg; d > 1e-9 || d < -1e-9 {
		t.Errorf("avg %v, flat %v", day.Avg, flat.Avg)
	}
}

func TestApproxP95(t *testing.T) {
	cases := []struct {
		name    string
		values  []float64
		weights []int
		want    float64
	}{
		{"weights of one are exact", []float64{5, 1, 4, 2, 3}, []int{1, 1, 1, 1, 1}, 5},
		{"exact 1..20", seq(20), ones(20), 19},
		{"heavy low bucket", []float64{100, 900}, []int{95, 5}, 100},
		{"just past the rank", []float64{100, 900}, []int{94, 6}, 900},
		{"zero weights ignored", []float64{1000, 10}, []int{0, 3}, 10},
		{"no weight", []float64{7}, []int{0}, 0},
		{"nothing", nil, nil, 0},
	}
	for _, c := range cases {
		if got := ApproxP95(c.values, c.weights); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func ones(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = 1
	}
	return out
}
