package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// addResults stores results of monitorID, one per second from start, with
// the given durations in ms; a negative duration is a failure.
func addResults(t testing.TB, d *db.DB, monitorID string, start time.Time, durations ...int) {
	t.Helper()
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, ms := range durations {
		r := CheckResult{MonitorID: monitorID, CheckedAt: start.Add(time.Duration(i) * time.Second), Success: ms >= 0}
		if ms >= 0 {
			r.Duration = time.Duration(ms) * time.Millisecond
		} else {
			r.Duration = time.Duration(-ms) * time.Millisecond
		}
		if err := InsertCheckResult(context.Background(), tx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func stats(ctx context.Context, d *db.DB, id string, from, to time.Time) (LatencyStats, error) {
	s, _, err := LatencyHistory(ctx, d.Reader, id, from, to, 100)
	return s, err
}

func series(ctx context.Context, d *db.DB, id string, from, to time.Time, buckets int) ([]LatencyPoint, error) {
	_, p, err := LatencyHistory(ctx, d.Reader, id, from, to, buckets)
	return p, err
}

func TestLatencyStats(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id, other := create(t, d, sample("api")), create(t, d, sample("other"))
	// 20 samples 1..20 ms, two failures (slow, excluded), then 7 ms last.
	var ds []int
	for i := 1; i <= 20; i++ {
		ds = append(ds, i)
	}
	ds = append(ds, -5000, -6000, 7)
	addResults(t, d, id, now, ds...)
	addResults(t, d, other, now, 1000, 1000)
	addResults(t, d, id, now.Add(-time.Hour), 9999) // before the range

	s, err := stats(ctx, d, id, now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	want := LatencyStats{Checks: 23, Failures: 2, Samples: 21, Current: 7 * ms, Min: ms, Max: 20 * ms,
		Avg: time.Duration(float64(217) / 21 * float64(ms)), P95: 19 * ms}
	if s != want {
		t.Fatalf("stats\n%+v\nwant\n%+v", s, want)
	}
	// The range end is exclusive.
	if s, _ := stats(ctx, d, id, now, now.Add(time.Second)); s.Checks != 1 || s.Max != ms {
		t.Fatalf("one-second range: %+v", s)
	}
	if s, err := stats(ctx, d, id, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil || s != (LatencyStats{}) {
		t.Fatalf("empty range: %+v, %v", s, err)
	}
	if s, _ := stats(ctx, d, id, now.Add(20*time.Second), now.Add(22*time.Second)); s.Checks != 2 || s.Samples != 0 || s.P95 != 0 {
		t.Fatalf("failures only: %+v", s)
	}
}

// p95 is the nearest rank, ceil(0.95 n), over the samples.
func TestLatencyP95NearestRank(t *testing.T) {
	for n, want := range map[int]int{1: 1, 2: 2, 19: 19, 20: 19, 21: 20, 100: 95, 101: 96} {
		d := testDB(t)
		id := create(t, d, sample("api"))
		var ds []int
		for i := n; i >= 1; i-- { // stored out of order
			ds = append(ds, i)
		}
		addResults(t, d, id, now, ds...)
		s, err := stats(context.Background(), d, id, now, now.Add(time.Hour))
		if err != nil || s.P95 != time.Duration(want)*time.Millisecond {
			t.Errorf("n = %d: p95 %v, %v; want %d ms", n, s.P95, err, want)
		}
	}
}

func TestLatencySeries(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("api"))
	// 0..9 s: 10, 20, failure, 40, then nothing until 8 s: 80, 90.
	addResults(t, d, id, now, 10, 20, -3000, 40)
	addResults(t, d, id, now.Add(8*time.Second), 80, 90)

	got, err := series(ctx, d, id, now, now.Add(10*time.Second), 5) // 2 s buckets
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	want := []LatencyPoint{
		{At: now, Checks: 2, Samples: 2, Avg: 15 * ms, Max: 20 * ms},
		{At: now.Add(2 * time.Second), Checks: 2, Failures: 1, Samples: 1, Avg: 40 * ms, Max: 40 * ms},
		{At: now.Add(8 * time.Second), Checks: 2, Samples: 2, Avg: 85 * ms, Max: 90 * ms},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Never more points than buckets, a bucket being at least a second.
	if got, _ := series(ctx, d, id, now, now.Add(10*time.Second), 1000); len(got) != 6 {
		t.Errorf("%d points with second buckets, want 6", len(got))
	}
	// 3 buckets of 4 s; the one from 4 s to 8 s holds nothing.
	if got, _ := series(ctx, d, id, now, now.Add(10*time.Second), 3); len(got) != 2 || !got[1].At.Equal(now.Add(8*time.Second)) {
		t.Errorf("3 buckets: %+v", got)
	}
	if got, _ := series(ctx, d, id, now, now, 10); got != nil {
		t.Errorf("empty range: %+v", got)
	}
}

// The range is read along the index, in its order: no sort.
func TestHistoryQueryPlan(t *testing.T) {
	d := testDB(t)
	plan := queryPlan(t, d.Reader, `SELECT checked_at, success, duration_ms FROM check_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ? ORDER BY checked_at`, "m", "a", "b")
	if !strings.Contains(plan, "idx_check_results_monitor_time") || strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("plan: %s", plan)
	}
}

func queryPlan(t *testing.T, q *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := q.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return strings.Join(plan, "; ")
}

// benchHistory is a week of raw results at the default 30 s interval, one
// in fifty failed: about 20,000 rows of the monitor among 20,000 of others.
func benchHistory(b *testing.B) (*db.DB, string) {
	d := testDB(b)
	id, other := create(b, d, sample("api")), create(b, d, sample("other"))
	for _, m := range []string{id, other} {
		tx, _ := d.Writer.Begin()
		for i := range 7 * 24 * 120 {
			r := CheckResult{MonitorID: m, CheckedAt: now.Add(time.Duration(i) * 30 * time.Second),
				Duration: time.Duration(50+i%200) * time.Millisecond, Success: i%50 != 0}
			if err := InsertCheckResult(context.Background(), tx, r); err != nil {
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
	return d, id
}

// BenchmarkLatencyHistory7d is the History tab's read for a week of raw
// results: summary and a 720-point series.
func BenchmarkLatencyHistory7d(b *testing.B) {
	d, id := benchHistory(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := LatencyHistory(context.Background(), d.Reader, id, now, now.Add(7*24*time.Hour), 720); err != nil {
			b.Fatal(err)
		}
	}
}
