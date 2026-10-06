package store

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/drilonrecica/sinjal/internal/history"
)

// History over check results (docs/09_DATABASE.md "History queries"). A
// range of one monitor is read once along idx_check_results_monitor_time
// and once along the check_aggregates key, and both the summary and the
// chart series are built from those two passes: one scan in Go is faster
// than SQL aggregates, a GROUP BY and an ORDER BY for the percentile.
// Rolling deletes the sources of what it writes, so every moment is in
// exactly one tier and the union counts nothing twice; both reads share one
// read transaction so a rollup committing in between cannot either.
// Latency is that of successful checks; a failure's duration is how long it
// took to fail, often a timeout.

// LatencyStats is the latency summary of a range.
type LatencyStats struct {
	Checks   int // results in the range
	Failures int
	Samples  int           // successful results with a duration
	Current  time.Duration // newest sample in the range
	Min, Avg time.Duration
	Max, P95 time.Duration
	// Approximate is set when the range holds rolled-up buckets: P95 is
	// then estimated (history.ApproxP95). Otherwise it is exact, the
	// nearest rank over the raw samples.
	Approximate bool
}

// LatencyPoint is one bucket of a latency series.
type LatencyPoint struct {
	At       time.Time // the bucket's start
	Checks   int
	Failures int
	Samples  int // successful results with a duration; Avg and Max need one
	Avg, Max time.Duration
}

// LatencyHistory returns the summary of [from, to) and its series: the
// range split into at most buckets equal buckets of whole seconds, those
// holding results, in order. A chart therefore draws at most that many
// points however many results the range holds. Raw results and rolled-up
// buckets are combined; a rolled bucket counts where it starts. Without
// samples every duration of the summary is zero.
func LatencyHistory(ctx context.Context, q *sql.DB, monitorID string, from, to time.Time, buckets int) (LatencyStats, []LatencyPoint, error) {
	var s LatencyStats
	from, to = from.Truncate(time.Second), to.Truncate(time.Second)
	span := int64(to.Sub(from) / time.Second)
	if span <= 0 || buckets <= 0 {
		return s, nil, nil
	}
	step := (span + int64(buckets) - 1) / int64(buckets) // seconds per bucket, at least 1
	tx, err := q.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s, nil, err
	}
	defer tx.Rollback()

	points := make([]LatencyPoint, (span+step-1)/step)
	sums := make([]float64, len(points)) // per point, for its average
	index := func(t time.Time) int { return int((t.Unix() - from.Unix()) / step) }

	rows, err := tx.QueryContext(ctx, `SELECT checked_at, success, duration_ms FROM check_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ? ORDER BY checked_at`,
		monitorID, formatTime(from), formatTime(to))
	if err != nil {
		return s, nil, err
	}
	defer rows.Close()
	var samples []float64
	var sum float64
	for rows.Next() {
		var at string
		var ok bool
		var ms sql.NullFloat64
		if err := rows.Scan(&at, &ok, &ms); err != nil {
			return s, nil, err
		}
		i := index(parseTime(at))
		p := &points[i]
		s.Checks++
		p.Checks++
		switch {
		case !ok:
			s.Failures++
			p.Failures++
		case ms.Valid:
			s.Current = msToDuration(ms.Float64)
			samples = append(samples, ms.Float64)
			sum += ms.Float64
			p.Samples++
			sums[i] += ms.Float64
			p.Max = max(p.Max, msToDuration(ms.Float64))
		}
	}
	if err := rows.Err(); err != nil {
		return s, nil, err
	}

	// Rolled buckets, scanned as plain numbers: at a year's range they are
	// about 15,000 rows, and NULL-aware scanning doubled their cost.
	rows, err = tx.QueryContext(ctx, `SELECT unixepoch(bucket_start), total_count, success_count, failure_count,
		coalesce(min_ms, 0), coalesce(max_ms, 0), coalesce(avg_ms, 0), coalesce(p95_ms, 0) FROM check_aggregates
		WHERE monitor_id = ? AND resolution_seconds IN (300, 3600, 86400) AND bucket_start >= ? AND bucket_start < ?`,
		monitorID, formatTime(from), formatTime(to))
	if err != nil {
		return s, nil, err
	}
	defer rows.Close()
	var lo, hi float64 // over the rolled buckets with samples
	var newestAt int64
	var newestAvg float64
	var p95s []float64
	var weights []int
	for rows.Next() {
		var at int64
		var b history.Bucket
		if err := rows.Scan(&at, &b.Total, &b.Success, &b.Failure, &b.Min, &b.Max, &b.Avg, &b.P95); err != nil {
			return s, nil, err
		}
		i := int((at - from.Unix()) / step)
		p := &points[i]
		s.Checks += b.Total
		s.Failures += b.Failure
		p.Checks += b.Total
		p.Failures += b.Failure
		if b.Success == 0 {
			continue
		}
		if len(p95s) == 0 {
			lo, hi = b.Min, b.Max
		}
		lo, hi = min(lo, b.Min), max(hi, b.Max)
		if at >= newestAt {
			newestAt, newestAvg = at, b.Avg
		}
		p95s, weights = append(p95s, b.P95), append(weights, b.Success)
		sum += b.Avg * float64(b.Success)
		p.Samples += b.Success
		sums[i] += b.Avg * float64(b.Success)
		p.Max = max(p.Max, msToDuration(b.Max))
	}
	if err := rows.Err(); err != nil {
		return s, nil, err
	}

	var out []LatencyPoint
	for i := range points {
		if points[i].Checks == 0 {
			continue
		}
		points[i].At = from.Add(time.Duration(int64(i)*step) * time.Second)
		if points[i].Samples > 0 {
			points[i].Avg = msToDuration(sums[i] / float64(points[i].Samples))
		}
		out = append(out, points[i])
	}

	n := len(samples)
	if n > 0 {
		slices.Sort(samples)
		s.Min, s.Max = msToDuration(samples[0]), msToDuration(samples[n-1])
		s.P95 = msToDuration(history.NearestRank(samples))
	}
	if len(p95s) > 0 {
		// Raw samples weigh one each, a bucket's p95 as many as its samples.
		s.Approximate = true
		if n == 0 {
			s.Min, s.Max, s.Current = msToDuration(lo), msToDuration(hi), msToDuration(newestAvg)
		} else {
			s.Min, s.Max = min(s.Min, msToDuration(lo)), max(s.Max, msToDuration(hi))
		}
		for _, w := range weights {
			n += w
		}
		values, ws := append(samples, p95s...), make([]int, len(samples), len(samples)+len(weights))
		for i := range ws {
			ws[i] = 1
		}
		s.P95 = msToDuration(history.ApproxP95(values, append(ws, weights...)))
	}
	if n > 0 {
		s.Samples = n
		s.Avg = msToDuration(sum / float64(n))
	}
	return s, out, nil
}
