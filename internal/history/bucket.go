package history

import (
	"cmp"
	"slices"
	"time"
)

// Rollup buckets (docs/09_DATABASE.md "Retention jobs", "p95"). Raw results
// older than 7 days become 5-minute buckets, 5-minute buckets older than 30
// days become hourly ones and hourly buckets older than 365 days daily ones.
// Buckets are aligned to the Unix epoch, so in UTC: a daily bucket is a UTC
// day and every bucket of a resolution has the same length, DST or not.
//
// Latency is that of successful checks, as for raw history; a failure's
// duration is how long it took to fail. Every stored result has a duration,
// so a bucket's Success is also its number of latency samples.

// Resolutions of the rollup tiers.
const (
	Res5m = 5 * time.Minute
	Res1h = time.Hour
	Res1d = 24 * time.Hour
)

// Bucket is one rolled-up bucket. Min, Max, Avg and P95 are milliseconds
// and mean something only when Success > 0; they are stored as NULL
// otherwise.
type Bucket struct {
	Start                   time.Time
	Total, Success, Failure int
	Min, Max, Avg, P95      float64
}

// Sample is one raw result as a bucket sees it.
type Sample struct {
	OK bool
	MS float64
}

// Floor is the start of the bucket of resolution res holding t.
func Floor(t time.Time, res time.Duration) time.Time {
	return t.UTC().Truncate(res)
}

// FromRaw builds a bucket from raw results. Its p95 is exact.
func FromRaw(start time.Time, samples []Sample) Bucket {
	b := Bucket{Start: start, Total: len(samples)}
	var ms []float64
	var sum float64
	for _, s := range samples {
		if !s.OK {
			b.Failure++
			continue
		}
		ms = append(ms, s.MS)
		sum += s.MS
	}
	if b.Success = len(ms); b.Success > 0 {
		slices.Sort(ms)
		b.Min, b.Max, b.Avg, b.P95 = ms[0], ms[len(ms)-1], sum/float64(len(ms)), NearestRank(ms)
	}
	return b
}

// Merge builds a bucket from finer ones: counts summed, the smallest
// minimum and largest maximum, the average weighted by successful checks.
// The p95 is approximate (ApproxP95 over the children's p95).
func Merge(start time.Time, children []Bucket) Bucket {
	b := Bucket{Start: start}
	var sum float64
	var p95s []float64
	var weights []int
	for _, c := range children {
		b.Total += c.Total
		b.Failure += c.Failure
		if c.Success == 0 {
			continue
		}
		if b.Success == 0 {
			b.Min, b.Max = c.Min, c.Max
		}
		b.Min, b.Max = min(b.Min, c.Min), max(b.Max, c.Max)
		b.Success += c.Success
		sum += c.Avg * float64(c.Success)
		p95s, weights = append(p95s, c.P95), append(weights, c.Success)
	}
	if b.Success > 0 {
		b.Avg = sum / float64(b.Success)
		b.P95 = ApproxP95(p95s, weights)
	}
	return b
}

// NearestRank is the exact p95 of sorted samples: the smallest one with at
// least 95 % of the samples at or below it, rank ceil(0.95 n). sorted must
// not be empty.
func NearestRank(sorted []float64) float64 {
	return sorted[rank95(len(sorted))-1]
}

// rank95 is ceil(0.95 n) in integers.
func rank95(n int) int { return (95*n + 99) / 100 }

// ApproxP95 is the weighted nearest rank over values: the smallest value at
// which the cumulative weight reaches ceil(0.95 × total weight). With every
// weight 1 it is the exact NearestRank. Applied to the p95 of buckets
// weighted by their samples it is an estimate, not a percentile of the
// underlying samples: it tends to overstate rather than understate, since
// a bucket's p95 is above most of its samples. values and weights have the
// same length; zero weights are ignored, and with no weight it is 0.
func ApproxP95(values []float64, weights []int) float64 {
	type pair struct {
		v float64
		w int
	}
	pairs := make([]pair, 0, len(values))
	total := 0
	for i, v := range values {
		if weights[i] > 0 {
			pairs = append(pairs, pair{v, weights[i]})
			total += weights[i]
		}
	}
	if total == 0 {
		return 0
	}
	slices.SortFunc(pairs, func(a, b pair) int { return cmp.Compare(a.v, b.v) })
	need, acc := rank95(total), 0
	for _, p := range pairs {
		if acc += p.w; acc >= need {
			return p.v
		}
	}
	return pairs[len(pairs)-1].v
}
