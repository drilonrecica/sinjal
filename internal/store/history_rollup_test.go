package store

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/history"
)

// addBuckets stores rolled-up buckets of resolution res as a rollup would.
func addBuckets(t testing.TB, d *db.DB, id string, res time.Duration, bs ...history.Bucket) {
	t.Helper()
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := upsertAggregates(context.Background(), tx, id, res, bs); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func ms(v float64) time.Duration { return msToDuration(v) }

func TestLatencyHistoryUnionsTiers(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id, other := create(t, d, sample("api")), create(t, d, sample("other"))
	day := history.Floor(now.AddDate(0, 0, -380), history.Res1d)
	hour := history.Floor(now.AddDate(0, 0, -100), history.Res1h)
	five := history.Floor(now.AddDate(0, 0, -10), history.Res5m)
	addBuckets(t, d, id, history.Res1d, history.Bucket{Start: day, Total: 100, Success: 90, Failure: 10, Min: 5, Max: 500, Avg: 50, P95: 400})
	addBuckets(t, d, id, history.Res1h, history.Bucket{Start: hour, Total: 120, Success: 120, Min: 20, Max: 80, Avg: 40, P95: 70})
	addBuckets(t, d, id, history.Res5m, history.Bucket{Start: five, Total: 10, Failure: 10}) // failures only
	addBuckets(t, d, other, history.Res1h, history.Bucket{Start: hour, Total: 9, Success: 9, Min: 1, Max: 9000, Avg: 9, P95: 9000})
	addResults(t, d, id, now.Add(-time.Hour), 10, 30, -5000)

	from, to := now.AddDate(0, 0, -400), now
	s, points, err := LatencyHistory(ctx, d.Reader, id, from, to, 400) // one point per day
	if err != nil {
		t.Fatal(err)
	}
	// Samples 90 + 120 + 2; average (90×50 + 120×40 + 10 + 30) / 212.
	// p95: rank ceil(0.95 × 212) = 202 over 10 (1), 30 (1), 70 (120),
	// 400 (90) reaches 400.
	want := LatencyStats{Checks: 233, Failures: 21, Samples: 212, Current: ms(30), Min: ms(5), Max: ms(500),
		Avg: ms(9340.0 / 212), P95: ms(400), Approximate: true}
	if s != want {
		t.Errorf("stats\n got %+v\nwant %+v", s, want)
	}
	if len(points) != 4 {
		t.Fatalf("%d points, want 4: %+v", len(points), points)
	}
	wantPoints := []LatencyPoint{
		{At: from.Add(time.Duration((day.Unix()-from.Unix())/86400) * 24 * time.Hour), Checks: 100, Failures: 10, Samples: 90, Avg: ms(50), Max: ms(500)},
		{At: from.Add(time.Duration((hour.Unix()-from.Unix())/86400) * 24 * time.Hour), Checks: 120, Samples: 120, Avg: ms(40), Max: ms(80)},
		{At: from.Add(time.Duration((five.Unix()-from.Unix())/86400) * 24 * time.Hour), Checks: 10, Failures: 10},
		{At: from.Add(399 * 24 * time.Hour), Checks: 3, Failures: 1, Samples: 2, Avg: ms(20), Max: ms(30)},
	}
	for i, p := range points {
		if p != wantPoints[i] {
			t.Errorf("point %d\n got %+v\nwant %+v", i, p, wantPoints[i])
		}
	}

	// A range of rolled buckets only: Current is the newest bucket's average.
	s, _, err = LatencyHistory(ctx, d.Reader, id, from, now.AddDate(0, 0, -50), 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.Checks != 220 || s.Current != ms(40) || s.Min != ms(5) || s.Max != ms(500) || !s.Approximate {
		t.Errorf("rolled only: %+v", s)
	}

	// A bucket counts where it starts: one starting before the range is out.
	s, _, err = LatencyHistory(ctx, d.Reader, id, hour.Add(time.Second), now.AddDate(0, 0, -50), 100)
	if err != nil {
		t.Fatal(err)
	}
	if s != (LatencyStats{}) {
		t.Errorf("bucket before the range counted: %+v", s)
	}

	// Raw results only: exact, as before rollups existed.
	s, _, err = LatencyHistory(ctx, d.Reader, id, now.AddDate(0, 0, -7), now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approximate || s.P95 != ms(30) || s.Checks != 3 {
		t.Errorf("raw only: %+v", s)
	}

	// Failures-only buckets lend no latency, so p95 stays exact.
	s, _, err = LatencyHistory(ctx, d.Reader, id, now.AddDate(0, 0, -20), now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approximate || s.Checks != 13 || s.Failures != 11 || s.Samples != 2 {
		t.Errorf("raw and failed bucket: %+v", s)
	}
}

func TestLatencyHistoryAverageAcrossTiers(t *testing.T) {
	// Rolling a range does not move its count, extremes or average.
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("api"))
	start := history.Floor(now.AddDate(0, 0, -8), history.Res1d)
	var ds []int
	for i := range 600 {
		if i%13 == 0 {
			ds = append(ds, -3000)
		} else {
			ds = append(ds, 10+i%90)
		}
	}
	addResults(t, d, id, start, ds...)
	from, to := start, start.Add(time.Hour)
	before, _, err := LatencyHistory(ctx, d.Reader, id, from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := RollupOldest(ctx, d, id, RawTier, history.Res5m, to, 24*time.Hour); err != nil || !ok {
		t.Fatalf("rollup: %v %v", ok, err)
	}
	after, _, err := LatencyHistory(ctx, d.Reader, id, from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if after.Checks != before.Checks || after.Failures != before.Failures || after.Samples != before.Samples ||
		after.Min != before.Min || after.Max != before.Max || !after.Approximate || before.Approximate {
		t.Errorf("before %+v\nafter %+v", before, after)
	}
	if diff := math.Abs(float64(after.Avg - before.Avg)); diff > float64(time.Microsecond) {
		t.Errorf("average moved from %v to %v", before.Avg, after.Avg)
	}
}

func TestLatencyHistoryAggregatePlan(t *testing.T) {
	d := testDB(t)
	plan := queryPlan(t, d.Reader, `SELECT bucket_start FROM check_aggregates
		WHERE monitor_id = ? AND resolution_seconds IN (300, 3600, 86400) AND bucket_start >= ? AND bucket_start < ?`, "m", "a", "b")
	if !strings.Contains(plan, "sqlite_autoindex_check_aggregates_1") || strings.Contains(plan, "SCAN check_aggregates") {
		t.Errorf("plan: %s", plan)
	}
}

// BenchmarkLatencyHistory1y is the History tab's read for a year at steady
// state: the week of raw results of benchHistory, 23 days of 5-minute
// buckets and 335 days of hourly ones before it.
func BenchmarkLatencyHistory1y(b *testing.B) {
	d, id := benchHistory(b)
	var fives, hours []history.Bucket
	for t := now.AddDate(0, 0, -23); t.Before(now); t = t.Add(history.Res5m) {
		fives = append(fives, history.Bucket{Start: t, Total: 10, Success: 10, Min: 40, Max: 240, Avg: 120, P95: 230})
	}
	for t := now.AddDate(0, 0, -358); t.Before(now.AddDate(0, 0, -23)); t = t.Add(history.Res1h) {
		hours = append(hours, history.Bucket{Start: t, Total: 120, Success: 118, Failure: 2, Min: 40, Max: 240, Avg: 120, P95: 230})
	}
	addBuckets(b, d, id, history.Res5m, fives...)
	addBuckets(b, d, id, history.Res1h, hours...)
	from, to := now.AddDate(0, 0, -358), now.Add(7*24*time.Hour)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := LatencyHistory(context.Background(), d.Reader, id, from, to, 720); err != nil {
			b.Fatal(err)
		}
	}
}
