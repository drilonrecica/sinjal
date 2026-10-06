package retention

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/history"
	"github.com/drilonrecica/sinjal/internal/store"
)

// now is a fixed run time: raw rolls before 2026-09-30T12:00Z, 5m before
// 2026-09-07T12:00Z, 1h before 2025-10-07T00:00Z.
var now = time.Date(2026, 10, 7, 12, 2, 0, 0, time.UTC)

func testDB(t testing.TB) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	return d
}

func newMonitor(t testing.TB, d *db.DB, name string) string {
	t.Helper()
	id, err := store.CreateMonitor(context.Background(), d, store.MonitorInput{Name: name, Enabled: true,
		HTTP: store.HTTPConfig{URL: "https://example.com/" + name}}, now.AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// raw is one check result: ms < 0 is a failure that took -ms.
type raw struct {
	at time.Time
	ms float64
}

func addRaw(t testing.TB, d *db.DB, id string, rs ...raw) {
	t.Helper()
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, r := range rs {
		ms := r.ms
		if ms < 0 {
			ms = -ms
		}
		if err := store.InsertCheckResult(context.Background(), tx, store.CheckResult{MonitorID: id, CheckedAt: r.at,
			Success: r.ms >= 0, Duration: time.Duration(ms * float64(time.Millisecond))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// addAgg stores buckets of resolution res directly, as an earlier run would.
func addAgg(t testing.TB, d *db.DB, id string, res time.Duration, bs ...history.Bucket) {
	t.Helper()
	for _, b := range bs {
		var lo, hi, avg, p95 any
		if b.Success > 0 {
			lo, hi, avg, p95 = b.Min, b.Max, b.Avg, b.P95
		}
		if _, err := d.Writer.Exec(`INSERT INTO check_aggregates VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, int(res/time.Second), store.FormatTime(b.Start), b.Total, b.Success, b.Failure, lo, hi, avg, p95); err != nil {
			t.Fatal(err)
		}
	}
}

// agg is a stored bucket as read back; NULL latency reads as nil.
type agg struct {
	Res                time.Duration
	Start              string
	Total, OK, Failed  int
	Min, Max, Avg, P95 *float64
}

func aggs(t testing.TB, d *db.DB, id string) []agg {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT resolution_seconds, bucket_start, total_count, success_count, failure_count,
		min_ms, max_ms, avg_ms, p95_ms FROM check_aggregates WHERE monitor_id = ?
		ORDER BY resolution_seconds, bucket_start`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []agg
	for rows.Next() {
		var a agg
		var res int
		var lo, hi, avg, p95 sql.NullFloat64
		if err := rows.Scan(&res, &a.Start, &a.Total, &a.OK, &a.Failed, &lo, &hi, &avg, &p95); err != nil {
			t.Fatal(err)
		}
		a.Res = time.Duration(res) * time.Second
		a.Min, a.Max, a.Avg, a.P95 = ptr(lo), ptr(hi), ptr(avg), ptr(p95)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func f(v float64) *float64 { return &v }

func rawTimes(t testing.TB, d *db.DB, id string) []string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT checked_at FROM check_results WHERE monitor_id = ? ORDER BY checked_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func run(t testing.TB, d *db.DB, when time.Time) Stats {
	t.Helper()
	st, err := Rollup(context.Background(), d, when)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCutoffs(t *testing.T) {
	want := []string{"2026-09-30T12:00:00Z", "2026-09-07T12:00:00Z", "2025-10-07T00:00:00Z"}
	for i, tier := range Tiers {
		if got := store.FormatTime(tier.Cutoff(now)); got != want[i] {
			t.Errorf("%s cutoff = %s, want %s", tier.Name, got, want[i])
		}
	}
}

func TestRollupRawIntoFiveMinutes(t *testing.T) {
	d := testDB(t)
	id := newMonitor(t, d, "api")
	addRaw(t, d, id,
		raw{at("2026-09-30T11:50:00Z"), -30000}, // a bucket of one failure
		raw{at("2026-09-30T11:55:00Z"), 10},
		raw{at("2026-09-30T11:57:30Z"), 30},
		raw{at("2026-09-30T11:59:59Z"), -5000}, // last second before the cutoff
		raw{at("2026-09-30T12:00:00Z"), 99},    // at the cutoff: kept raw
		raw{at("2026-10-07T12:00:00Z"), 42},
	)
	st := run(t, d, now)
	want := []agg{
		{Res: history.Res5m, Start: "2026-09-30T11:50:00Z", Total: 1, Failed: 1},
		{Res: history.Res5m, Start: "2026-09-30T11:55:00Z", Total: 3, OK: 2, Failed: 1, Min: f(10), Max: f(30), Avg: f(20), P95: f(30)},
	}
	if got := aggs(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("aggregates\n got %+v\nwant %+v", got, want)
	}
	if got := rawTimes(t, d, id); !reflect.DeepEqual(got, []string{"2026-09-30T12:00:00Z", "2026-10-07T12:00:00Z"}) {
		t.Errorf("raw left %v", got)
	}
	if st != (Stats{Steps: 1, Buckets: 2, Deleted: 4}) {
		t.Errorf("stats %+v", st)
	}
}

func TestRollupHourAndDayTiers(t *testing.T) {
	d := testDB(t)
	id := newMonitor(t, d, "api")
	five := func(s string, total, ok int, lo, hi, avg, p95 float64) history.Bucket {
		return history.Bucket{Start: at(s), Total: total, Success: ok, Failure: total - ok, Min: lo, Max: hi, Avg: avg, P95: p95}
	}
	addAgg(t, d, id, history.Res5m,
		five("2026-09-07T11:00:00Z", 10, 10, 20, 80, 40, 70),
		five("2026-09-07T11:55:00Z", 10, 8, 10, 300, 70, 250), // last bucket before the 5m cutoff
		five("2026-09-07T12:00:00Z", 10, 10, 5, 5, 5, 5),      // at the cutoff: kept
	)
	addAgg(t, d, id, history.Res1h,
		five("2025-10-06T00:00:00Z", 120, 120, 30, 90, 50, 80),
		five("2025-10-06T23:00:00Z", 120, 0, 0, 0, 0, 0),   // all failed
		five("2025-10-07T00:00:00Z", 120, 120, 1, 1, 1, 1), // at the 1h cutoff: kept
	)
	run(t, d, now)
	want := []agg{
		{Res: history.Res5m, Start: "2026-09-07T12:00:00Z", Total: 10, OK: 10, Min: f(5), Max: f(5), Avg: f(5), P95: f(5)},
		// avg (10×40 + 8×70) / 18 = 53.33…; p95 rank ceil(0.95×18)=18 → 250.
		{Res: history.Res1h, Start: "2025-10-07T00:00:00Z", Total: 120, OK: 120, Min: f(1), Max: f(1), Avg: f(1), P95: f(1)},
		{Res: history.Res1h, Start: "2026-09-07T11:00:00Z", Total: 20, OK: 18, Failed: 2, Min: f(10), Max: f(300), Avg: f(960.0 / 18), P95: f(250)},
		{Res: history.Res1d, Start: "2025-10-06T00:00:00Z", Total: 240, OK: 120, Failed: 120, Min: f(30), Max: f(90), Avg: f(50), P95: f(80)},
	}
	if got := aggs(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("aggregates\n got %+v\nwant %+v", got, want)
	}
}

func TestRollupChainsThroughEveryTier(t *testing.T) {
	// Raw results older than a year go all the way to one daily bucket in
	// one run, with every count, the extremes and the average kept.
	d := testDB(t)
	id := newMonitor(t, d, "api")
	day := at("2025-06-01T00:00:00Z")
	var rs []raw
	for i := range 2880 { // a day at 30 s
		ms := float64(10 + i%50)
		if i%97 == 0 {
			ms = -2000
		}
		rs = append(rs, raw{day.Add(time.Duration(i) * 30 * time.Second), ms})
	}
	addRaw(t, d, id, rs...)
	run(t, d, now)
	got := aggs(t, d, id)
	if len(got) != 1 || got[0].Res != history.Res1d || got[0].Start != "2025-06-01T00:00:00Z" {
		t.Fatalf("aggregates %+v", got)
	}
	b := got[0]
	var ok int
	var sum float64
	for i := range 2880 {
		if i%97 != 0 {
			ok++
			sum += float64(10 + i%50)
		}
	}
	if b.Total != 2880 || b.OK != ok || b.Failed != 2880-ok || *b.Min != 10 || *b.Max != 59 {
		t.Errorf("day bucket %+v", b)
	}
	if d := *b.Avg - sum/float64(ok); d > 1e-9 || d < -1e-9 {
		t.Errorf("avg %v, want %v", *b.Avg, sum/float64(ok))
	}
	if n := len(rawTimes(t, d, id)); n != 0 {
		t.Errorf("%d raw rows left", n)
	}
}

func TestRollupIsIdempotent(t *testing.T) {
	d := testDB(t)
	id := newMonitor(t, d, "api")
	addRaw(t, d, id, raw{at("2026-09-20T10:00:00Z"), 10}, raw{at("2026-09-21T10:00:00Z"), 20}, raw{at("2026-10-07T10:00:00Z"), 30})
	run(t, d, now)
	before, raws := aggs(t, d, id), rawTimes(t, d, id)
	if st := run(t, d, now); st != (Stats{}) {
		t.Errorf("second run did %+v", st)
	}
	if got := aggs(t, d, id); !reflect.DeepEqual(got, before) {
		t.Errorf("second run changed aggregates\n got %+v\nwant %+v", got, before)
	}
	if got := rawTimes(t, d, id); !reflect.DeepEqual(got, raws) {
		t.Errorf("second run changed raw rows: %v", got)
	}
}

func TestRollupSlicesAndSkipsGaps(t *testing.T) {
	d := testDB(t)
	id, other := newMonitor(t, d, "api"), newMonitor(t, d, "web")
	// Three consecutive days, then nothing for a week, then one more day.
	for _, day := range []string{"2026-09-20", "2026-09-21", "2026-09-22", "2026-09-28"} {
		addRaw(t, d, id, raw{at(day + "T06:00:00Z"), 10}, raw{at(day + "T18:00:00Z"), 20})
	}
	addRaw(t, d, other, raw{at("2026-10-07T11:00:00Z"), 5}) // recent: not rolled
	st := run(t, d, now)
	// One transaction per day of data, none for the empty days between.
	if st.Steps != 4 || st.Deleted != 8 || st.Buckets != 8 {
		t.Errorf("stats %+v, want 4 steps, 8 buckets, 8 rows", st)
	}
	if got := aggs(t, d, other); len(got) != 0 {
		t.Errorf("other monitor rolled: %+v", got)
	}
	if got := rawTimes(t, d, other); len(got) != 1 {
		t.Errorf("other monitor raw rows %v", got)
	}
}

func TestRollupStoppedRunResumes(t *testing.T) {
	// A run that stops after its first step (shutdown, an error) leaves
	// what a full run leaves once the next run has finished the job.
	seedDB := func() (*db.DB, string) {
		d := testDB(t)
		id := newMonitor(t, d, "api")
		for i := range 5 {
			day := at("2026-09-10T00:00:00Z").AddDate(0, 0, i)
			addRaw(t, d, id, raw{day.Add(time.Hour), float64(10 + i)}, raw{day.Add(2 * time.Hour), -100})
		}
		return d, id
	}
	full, fid := seedDB()
	run(t, full, now)

	stopped, sid := seedDB()
	tier := Tiers[0]
	if _, ok, err := store.RollupOldest(context.Background(), stopped, sid, tier.Src, tier.Dst, tier.Cutoff(now), Slice); err != nil || !ok {
		t.Fatalf("first step: ok %v, %v", ok, err)
	}
	if n := len(rawTimes(t, stopped, sid)); n != 8 {
		t.Fatalf("after one step %d raw rows, want 8", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Rollup(ctx, stopped, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run: %v", err)
	}
	if n := len(rawTimes(t, stopped, sid)); n != 8 {
		t.Fatalf("a cancelled run changed raw rows: %d", n)
	}
	if st := run(t, stopped, now); st.Steps != 4 {
		t.Errorf("resumed run took %d steps, want 4", st.Steps)
	}
	if got, want := aggs(t, stopped, sid), aggs(t, full, fid); !reflect.DeepEqual(got, want) {
		t.Errorf("resumed\n got %+v\nwant %+v", got, want)
	}
}
