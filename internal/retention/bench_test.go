package retention

import (
	"context"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/history"
)

// BenchmarkRollup is performance scenario 5 (docs/18): one daily run over
// one monitor that has a day's backlog in two tiers — three days of raw
// results at 30 s (8,640 rows into 5-minute buckets) and ten days of
// 5-minute buckets (2,880 into hourly buckets). The seeding is outside the
// timer; each iteration rolls a fresh database, as rolling consumes its
// sources.
func BenchmarkRollup(b *testing.B) {
	ctx := context.Background()
	var rows, buckets int64
	for b.Loop() {
		b.StopTimer()
		d := testDB(b)
		id := newMonitor(b, d, "api")
		var rs []raw
		for t := now.AddDate(0, 0, -10); t.Before(now.AddDate(0, 0, -7)); t = t.Add(30 * time.Second) {
			rs = append(rs, raw{t, 120})
		}
		addRaw(b, d, id, rs...)
		var fives []history.Bucket
		for t := now.AddDate(0, 0, -40); t.Before(now.AddDate(0, 0, -30)); t = t.Add(history.Res5m) {
			fives = append(fives, history.Bucket{Start: t, Total: 10, Success: 10, Min: 40, Max: 240, Avg: 120, P95: 230})
		}
		addAgg(b, d, id, history.Res5m, fives...)
		b.StartTimer()

		st, err := Rollup(ctx, d, now)
		if err != nil {
			b.Fatal(err)
		}
		rows, buckets = st.Deleted, int64(st.Buckets)
	}
	b.ReportMetric(float64(rows), "rows-rolled")
	b.ReportMetric(float64(buckets), "buckets-written")
}
