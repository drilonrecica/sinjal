package results

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// BenchmarkProcessorBatch is docs/18 scenario 3: results of 1,000 monitors
// through the processor into a real SQLite file, with the production batch
// size and flush time. One operation is one result, from Add until it is
// committed (state machine, raw row and monitor row update included).
func BenchmarkProcessorBatch(b *testing.B) {
	d, _ := testDB(b)
	const monitors = 1000
	ids := make([]string, monitors)
	for i := range ids {
		ids[i] = newMonitor(b, d, fmt.Sprintf("m%04d", i), nil)
	}
	p := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), func(string, time.Duration) {}, func(string) {}, func(incident.Intent) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	start := time.Now()
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		// Mostly successes, one failure in ten, so state changes and
		// retries happen as they would.
		p.Add(ctx, Result{MonitorID: ids[n%monitors], CheckedAt: start.Add(time.Duration(n) * time.Millisecond),
			Duration: 42 * time.Millisecond, Success: n%10 != 0, Status: "200"})
		n++
	}
	for p.Stats().Persisted < uint64(n) {
		time.Sleep(time.Millisecond)
	}
	b.ReportMetric(float64(n)/time.Since(start).Seconds(), "results/s")
}
