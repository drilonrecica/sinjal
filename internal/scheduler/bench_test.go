package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Benchmarks for docs/18 scenarios 1 (scheduler insert/update/pop) and 2
// (1,000 due monitors through the bounded worker pool).

var benchSizes = []int{1000, 10000}

// filledQueue holds n monitors with 30 s intervals, first runs spread over
// one interval.
func filledQueue(n int) (*queue, []string) {
	q := &queue{}
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("m%05d", i)
		q.set(t0, ids[i], 30*s, time.Duration(i)*30*s/time.Duration(n))
	}
	return q, ids
}

// An add on a queue of n: a new monitor goes in (and out again, so the
// size stays n).
func BenchmarkQueueInsert(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			q, _ := filledQueue(n)
			b.ReportAllocs()
			for b.Loop() {
				q.set(t0, "new", 30*s, 15*s)
				q.remove("new")
			}
		})
	}
}

// An edit: an existing monitor gets a new interval and an immediate check.
func BenchmarkQueueUpdate(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			q, ids := filledQueue(n)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				q.set(t0, ids[i%n], 60*s, 0)
				i++
			}
		})
	}
}

// Steady state: the clock moves on by one n-th of the interval per pop, so
// every pop finds one due monitor and moves it to its next run.
func BenchmarkQueuePop(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			q, _ := filledQueue(n)
			step := 30 * s / time.Duration(n)
			now := t0
			popped := 0
			b.ReportAllocs()
			for b.Loop() {
				now = now.Add(step)
				if _, ok := q.popDue(now); ok {
					popped++
				}
			}
			if popped == 0 {
				b.Fatal("nothing was due")
			}
		})
	}
}

// BenchmarkDueMonitors: 1,000 monitors due at the same instant go through
// the pool with the default worker count; each check takes 10 ms. One
// operation is the whole burst. The queue holds them all (nothing rejected),
// and the goroutine count is the workers, not the monitors.
func BenchmarkDueMonitors(b *testing.B) {
	const monitors, latency = 1000, 10 * time.Millisecond
	workers := min(32, max(8, runtime.NumCPU()*4)) // config.defaultWorkers
	base := runtime.NumGoroutine()
	var wg sync.WaitGroup
	p := NewPool(workers, 0, func(ctx context.Context, j Job) {
		defer wg.Done()
		select {
		case <-time.After(latency):
		case <-ctx.Done():
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { p.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	peak := 0
	b.ReportAllocs()
	for b.Loop() {
		due := time.Now()
		wg.Add(monitors)
		for i := range monitors {
			p.Submit(Job{MonitorID: fmt.Sprint(i), Due: due})
		}
		peak = max(peak, runtime.NumGoroutine())
		wg.Wait()
	}
	st := p.Stats()
	if st.Rejected != 0 {
		b.Fatalf("%d jobs rejected", st.Rejected)
	}
	// Workers plus Run and this benchmark's helper; a goroutine per
	// monitor would add a thousand.
	if extra := peak - base; extra > workers+4 {
		b.Fatalf("%d goroutines for %d workers", extra, workers)
	}
	b.ReportMetric(float64(workers), "workers")
	b.ReportMetric(float64(peak-base), "goroutines")
	b.ReportMetric(float64(st.LateStarts)/float64(b.N), "late-starts/op")
}
