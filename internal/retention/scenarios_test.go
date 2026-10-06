package retention

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/history"
	"github.com/drilonrecica/sinjal/internal/store"
)

// Scenarios 12 (retention rollup) and 13 (raw deletion only after a
// successful aggregate write) of docs/20, plus the bounded size of the
// database over a simulated year.

// simInterval is the synthetic check interval: 72 results a day, one in
// every fourth 5-minute bucket, three per hour, 72 per day. Coarse enough
// to simulate more than a year within the race detector's budget (CI runs
// only with it), fine enough that the hourly and daily tiers merge several
// sources per bucket.
const simInterval = 20 * time.Minute

// simLatency is the synthetic result i: about one in 53 fails, and so does
// the whole first hour of day 100 (a failures-only stretch).
func simLatency(start time.Time, i int) (at time.Time, ok bool, ms float64) {
	at = start.Add(time.Duration(i) * simInterval)
	day := int(at.Sub(start) / (24 * time.Hour))
	ok = i%53 != 0 && !(day == 100 && at.Sub(start)%(24*time.Hour) < time.Hour)
	return at, ok, float64(20 + (i*7919)%300)
}

// totals is what has been stored, to compare with what the tiers hold.
type totals struct {
	checks, failures, samples int
	sum, lo, hi               float64
}

func (s *totals) add(ok bool, ms float64) {
	s.checks++
	if !ok {
		s.failures++
		return
	}
	if s.samples == 0 || ms < s.lo {
		s.lo = ms
	}
	s.hi = max(s.hi, ms)
	s.samples++
	s.sum += ms
}

// stored is the same figures read back across raw results and every tier.
func stored(t testing.TB, d *db.DB, id string) totals {
	t.Helper()
	var s totals
	var rawLo, rawHi, aggLo, aggHi, rawSum, aggSum *float64
	var rawChecks, rawFailed, rawOK, aggChecks, aggFailed, aggOK int
	if err := d.Reader.QueryRow(`SELECT count(*), count(*) - coalesce(sum(success), 0), coalesce(sum(success), 0),
		min(CASE WHEN success THEN duration_ms END), max(CASE WHEN success THEN duration_ms END),
		sum(CASE WHEN success THEN duration_ms END) FROM check_results WHERE monitor_id = ?`, id).
		Scan(&rawChecks, &rawFailed, &rawOK, &rawLo, &rawHi, &rawSum); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.QueryRow(`SELECT coalesce(sum(total_count), 0), coalesce(sum(failure_count), 0),
		coalesce(sum(success_count), 0), min(min_ms), max(max_ms), sum(avg_ms * success_count)
		FROM check_aggregates WHERE monitor_id = ?`, id).
		Scan(&aggChecks, &aggFailed, &aggOK, &aggLo, &aggHi, &aggSum); err != nil {
		t.Fatal(err)
	}
	s.checks, s.failures, s.samples = rawChecks+aggChecks, rawFailed+aggFailed, rawOK+aggOK
	first := true
	for _, v := range []*float64{rawLo, aggLo} {
		if v != nil && (first || *v < s.lo) {
			s.lo, first = *v, false
		}
	}
	for _, v := range []*float64{rawHi, aggHi} {
		if v != nil {
			s.hi = max(s.hi, *v)
		}
	}
	for _, v := range []*float64{rawSum, aggSum} {
		if v != nil {
			s.sum += *v
		}
	}
	return s
}

// tierSpan is the oldest and newest start held at a resolution, or of raw
// results for store.RawTier: two index probes. ok is false when it is empty.
func tierSpan(t testing.TB, d *db.DB, id string, res time.Duration) (oldest, newest string, ok bool) {
	t.Helper()
	q, args := `FROM check_results WHERE monitor_id = ?`, []any{id}
	col := "checked_at"
	if res != store.RawTier {
		q, args, col = `FROM check_aggregates WHERE monitor_id = ? AND resolution_seconds = ?`, []any{id, int(res / time.Second)}, "bucket_start"
	}
	var lo, hi *string
	if err := d.Reader.QueryRow(`SELECT min(`+col+`) `+q, args...).Scan(&lo); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.QueryRow(`SELECT max(`+col+`) `+q, args...).Scan(&hi); err != nil {
		t.Fatal(err)
	}
	if lo == nil {
		return "", "", false
	}
	return *lo, *hi, true
}

// tierRows counts the rows at a resolution, or raw results for store.RawTier.
func tierRows(t testing.TB, d *db.DB, id string, res time.Duration) int {
	t.Helper()
	var n int
	var err error
	if res == store.RawTier {
		err = d.Reader.QueryRow(`SELECT count(*) FROM check_results WHERE monitor_id = ?`, id).Scan(&n)
	} else {
		err = d.Reader.QueryRow(`SELECT count(*) FROM check_aggregates WHERE monitor_id = ? AND resolution_seconds = ?`,
			id, int(res/time.Second)).Scan(&n)
	}
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// pages is the database's size in pages and how many of them are free.
func pages(t testing.TB, d *db.DB) (total, free int) {
	t.Helper()
	if err := d.Reader.QueryRow(`PRAGMA page_count`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	return total, free
}

// TestScenario12SimulatedYear stores 425 days of results at 20-minute
// intervals, one day at a time, and runs the rollup after each day as the
// daily job would. After every run each tier holds exactly its age range,
// and counts, extremes and the average are what was stored. At steady
// state (past 365 days) the database stops growing.
func TestScenario12SimulatedYear(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates 425 days of results")
	}
	const days = 425
	d := testDB(t)
	id := newMonitor(t, d, "api")
	ctx := context.Background()
	start := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)
	perDay := int(24 * time.Hour / simInterval)
	var want totals
	sizes := map[int]int{}
	for day := range days {
		tx, err := d.Writer.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for i := day * perDay; i < (day+1)*perDay; i++ {
			at, ok, ms := simLatency(start, i)
			want.add(ok, ms)
			if err := store.InsertCheckResult(ctx, tx, store.CheckResult{MonitorID: id, CheckedAt: at, Success: ok,
				Duration: time.Duration(ms * float64(time.Millisecond))}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		now := start.AddDate(0, 0, day+1).Add(4 * time.Hour) // the next day's 04:00 run
		if _, err := Rollup(ctx, d, now); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}

		// Each tier holds its age range only.
		raw, five, hour := Tiers[0].Cutoff(now), Tiers[1].Cutoff(now), Tiers[2].Cutoff(now)
		check := func(name string, res time.Duration, from, to time.Time) {
			oldest, newest, ok := tierSpan(t, d, id, res)
			if !ok {
				return
			}
			if (!from.IsZero() && oldest < store.FormatTime(from)) || (!to.IsZero() && newest >= store.FormatTime(to)) {
				t.Fatalf("day %d: %s holds %s to %s, want within [%v, %v)", day, name, oldest, newest, from, to)
			}
		}
		check("raw", store.RawTier, raw, time.Time{})
		check("5m", history.Res5m, five, raw)
		check("1h", history.Res1h, hour, five)
		check("1d", history.Res1d, time.Time{}, hour)

		if day%25 == 0 || day == days-1 {
			got := stored(t, d, id)
			if got.checks != want.checks || got.failures != want.failures || got.samples != want.samples ||
				got.lo != want.lo || got.hi != want.hi {
				t.Fatalf("day %d: stored %+v, want %+v", day, got, want)
			}
			if math.Abs(got.sum-want.sum) > 1e-6*want.sum {
				t.Fatalf("day %d: latency sum %v, want %v", day, got.sum, want.sum)
			}
		}
		if day >= 380 {
			total, free := pages(t, d)
			sizes[day] = total - free
			if day == 380 || day == days-1 {
				t.Logf("day %d: %d pages, %d free", day, total, free)
			}
		}
	}

	// Every tier is populated at the end: 7 days raw, 23 of 5m, 335 of 1h,
	// the rest daily.
	end := start.AddDate(0, 0, days).Add(4 * time.Hour)
	for _, tc := range []struct {
		name string
		res  time.Duration
		n    int
	}{
		{"raw", store.RawTier, int(start.AddDate(0, 0, days).Sub(Tiers[0].Cutoff(end)) / simInterval)},
		{"5m", history.Res5m, int(Tiers[0].Cutoff(end).Sub(Tiers[1].Cutoff(end)) / simInterval)}, // a result in every fourth bucket
		{"1h", history.Res1h, int(Tiers[1].Cutoff(end).Sub(Tiers[2].Cutoff(end)) / history.Res1h)},
		{"1d", history.Res1d, int(Tiers[2].Cutoff(end).Sub(start) / history.Res1d)},
	} {
		if n := tierRows(t, d, id, tc.res); n != tc.n {
			t.Errorf("%s holds %d rows, want %d", tc.name, n, tc.n)
		}
	}

	// Bounded size: past a year only the daily tier grows, one row a day,
	// so the pages in use stay where they were.
	if grew := sizes[days-1] - sizes[380]; grew > sizes[380]/50+4 {
		t.Errorf("database grew by %d pages from day 380 (%d) to day %d (%d)", grew, sizes[380], days-1, sizes[days-1])
	}
}

// snapshot is every row of check_results and check_aggregates of the one
// monitor of a test database, as text (its id differs between databases).
func snapshot(t testing.TB, d *db.DB) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT checked_at, success, duration_ms FROM check_results ORDER BY checked_at`,
		`SELECT resolution_seconds, bucket_start, total_count, success_count, failure_count, min_ms, max_ms, avg_ms, p95_ms
			FROM check_aggregates ORDER BY resolution_seconds, bucket_start`,
	} {
		rows, err := d.Reader.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintln(vals...))
		}
		rows.Close()
	}
	return out
}

// TestScenario13FailureKeepsSources injects a failure into each statement
// of a rollup step with a trigger (no hook in the product): the run
// reports it and nothing changed, neither the sources nor the buckets.
// Without the trigger the next run rolls everything, as a run that never
// failed does.
func TestScenario13FailureKeepsSources(t *testing.T) {
	cases := []struct {
		name, trigger string
	}{
		{"bucket write fails", `CREATE TRIGGER fail BEFORE INSERT ON check_aggregates
			BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"raw deletion fails after the bucket write", `CREATE TRIGGER fail BEFORE DELETE ON check_results
			BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"5m deletion fails after the hourly write", `CREATE TRIGGER fail BEFORE DELETE ON check_aggregates
			WHEN old.resolution_seconds = 300 BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"hourly write fails", `CREATE TRIGGER fail BEFORE INSERT ON check_aggregates
			WHEN new.resolution_seconds = 3600 BEGIN SELECT RAISE(ABORT, 'injected'); END`},
	}
	seedDB := func(t *testing.T) *db.DB {
		d := testDB(t)
		id := newMonitor(t, d, "api")
		// Only rows of one tier per case are due, so the failing step is
		// the first one: raw from 10 days ago, or 5m from 40 days ago.
		var rs []raw
		for i := range 300 {
			at := now.AddDate(0, 0, -10).Add(time.Duration(i) * time.Minute)
			ms := float64(10 + i%40)
			if i%17 == 0 {
				ms = -900
			}
			rs = append(rs, raw{at, ms})
		}
		addRaw(t, d, id, rs...)
		for i := range 24 {
			addAgg(t, d, id, history.Res5m, history.Bucket{Start: now.AddDate(0, 0, -40).Truncate(time.Hour).Add(time.Duration(i) * history.Res5m),
				Total: 10, Success: 9, Failure: 1, Min: 5, Max: float64(50 + i), Avg: 20, P95: 45})
		}
		return d
	}
	control := seedDB(t)
	run(t, control, now)
	want := snapshot(t, control)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := seedDB(t)
			if _, err := d.Writer.Exec(c.trigger); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, d)
			st, err := Rollup(context.Background(), d, now)
			if err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("run error = %v, want the injected failure", err)
			}
			got := snapshot(t, d)
			if c.name == "bucket write fails" || c.name == "raw deletion fails after the bucket write" {
				// The raw step fails first: nothing at all changed.
				if !reflect.DeepEqual(got, before) || st.Steps != 0 {
					t.Fatalf("a failed run changed data (%d steps committed)", st.Steps)
				}
			} else {
				// The raw tier rolled; the failing 5m step left its
				// buckets and wrote no hourly one.
				var n int
				if err := d.Reader.QueryRow(`SELECT count(*) FROM check_aggregates WHERE resolution_seconds = 300
					AND bucket_start < ?`, store.FormatTime(Tiers[1].Cutoff(now))).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 24 {
					t.Errorf("%d due 5m buckets left of 24", n)
				}
				if n := countRes(t, d, history.Res1h); n != 0 {
					t.Errorf("%d hourly buckets written by a failed step", n)
				}
			}
			if _, err := d.Writer.Exec(`DROP TRIGGER fail`); err != nil {
				t.Fatal(err)
			}
			run(t, d, now)
			if got := snapshot(t, d); !reflect.DeepEqual(got, want) {
				t.Errorf("after the failure and a clean run:\n got %v\nwant %v", got, want)
			}
		})
	}
}

func countRes(t testing.TB, d *db.DB, res time.Duration) int {
	t.Helper()
	var n int
	if err := d.Reader.QueryRow(`SELECT count(*) FROM check_aggregates WHERE resolution_seconds = ?`, int(res/time.Second)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
