package store

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

// History over raw check results (docs/09_DATABASE.md "History queries").
// A range of one monitor is read once along idx_check_results_monitor_time
// and both the summary and the chart series are built from that pass: one
// scan in Go is faster than SQL aggregates, a GROUP BY and an ORDER BY for
// the percentile. Latency is that of successful checks; a failure's
// duration is how long it took to fail, often a timeout. Ranges older than
// raw retention will union the M6 aggregates.

// LatencyStats is the latency summary of a range.
type LatencyStats struct {
	Checks   int // results in the range
	Failures int
	Samples  int           // successful results with a duration
	Current  time.Duration // newest sample in the range
	Min, Avg time.Duration
	Max, P95 time.Duration // P95 is exact: nearest rank over the samples
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
// points however many results the range holds. Without samples every
// duration of the summary is zero.
func LatencyHistory(ctx context.Context, q *sql.DB, monitorID string, from, to time.Time, buckets int) (LatencyStats, []LatencyPoint, error) {
	var s LatencyStats
	from, to = from.Truncate(time.Second), to.Truncate(time.Second)
	span := int64(to.Sub(from) / time.Second)
	if span <= 0 || buckets <= 0 {
		return s, nil, nil
	}
	step := (span + int64(buckets) - 1) / int64(buckets) // seconds per bucket, at least 1
	rows, err := q.QueryContext(ctx, `SELECT checked_at, success, duration_ms FROM check_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ? ORDER BY checked_at`,
		monitorID, formatTime(from), formatTime(to))
	if err != nil {
		return s, nil, err
	}
	defer rows.Close()
	var samples []float64
	var sum float64
	var points []LatencyPoint
	var sums []float64 // per point, for its average
	bucket := int64(-1)
	for rows.Next() {
		var at string
		var ok bool
		var ms sql.NullFloat64
		if err := rows.Scan(&at, &ok, &ms); err != nil {
			return s, nil, err
		}
		if b := (parseTime(at).Unix() - from.Unix()) / step; b != bucket {
			bucket = b
			points = append(points, LatencyPoint{At: from.Add(time.Duration(b*step) * time.Second)})
			sums = append(sums, 0)
		}
		p := &points[len(points)-1]
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
			sums[len(sums)-1] += ms.Float64
			p.Max = max(p.Max, msToDuration(ms.Float64))
		}
	}
	if err := rows.Err(); err != nil {
		return s, nil, err
	}
	for i := range points {
		if points[i].Samples > 0 {
			points[i].Avg = msToDuration(sums[i] / float64(points[i].Samples))
		}
	}
	if n := len(samples); n > 0 {
		slices.Sort(samples)
		s.Samples = n
		s.Min, s.Max, s.Avg = msToDuration(samples[0]), msToDuration(samples[n-1]), msToDuration(sum/float64(n))
		// Nearest rank: the smallest sample with at least 95 % of the
		// samples at or below it, rank ceil(0.95 n).
		s.P95 = msToDuration(samples[(95*n+99)/100-1])
	}
	return s, points, nil
}
