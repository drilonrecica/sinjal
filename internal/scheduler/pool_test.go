package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// syncBuffer is a log sink that is safe to read while workers write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startPool runs a pool until the test ends.
func startPool(t *testing.T, workers, queue int, run func(context.Context, Job)) (*Pool, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	p := NewPool(workers, queue, run, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return p, logs
}

func job(id string) Job { return Job{MonitorID: id, Due: time.Now()} }

func TestPoolBoundsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var running, peak, ran atomic.Int64
		p, _ := startPool(t, 4, 0, func(context.Context, Job) {
			n := running.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Second)
			running.Add(-1)
			ran.Add(1)
		})
		for i := range 20 {
			p.Submit(job(fmt.Sprint(i)))
		}
		synctest.Wait()
		if st := p.Stats(); st.Workers != 4 || st.Active != 4 || st.Queued != 16 {
			t.Fatalf("stats with all workers busy: %+v", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if ran.Load() != 20 || peak.Load() != 4 {
			t.Fatalf("ran %d jobs, peak concurrency %d", ran.Load(), peak.Load())
		}
		if st := p.Stats(); st.Active != 0 || st.Queued != 0 || st.Rejected != 0 {
			t.Fatalf("stats when idle: %+v", st)
		}
	})
}

func TestPoolRunsRetriesFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var order []string
		p, _ := startPool(t, 1, 0, func(_ context.Context, j Job) {
			time.Sleep(time.Second)
			mu.Lock()
			order = append(order, j.MonitorID)
			mu.Unlock()
		})
		p.Submit(job("first"))
		synctest.Wait() // the only worker is busy with "first"
		p.Submit(job("a"))
		p.Submit(job("b"))
		p.Submit(Job{MonitorID: "retry-1", Due: time.Now(), Retry: true})
		p.Submit(job("c"))
		p.Submit(Job{MonitorID: "retry-2", Due: time.Now(), Retry: true})
		time.Sleep(10 * time.Second)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if got := strings.Join(order, " "); got != "first retry-1 retry-2 a b c" {
			t.Fatalf("order: %s", got)
		}
	})
}

func TestPoolQueueIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var ran atomic.Int64
		p, logs := startPool(t, 1, 3, func(context.Context, Job) {
			<-release
			ran.Add(1)
		})
		p.Submit(job("running"))
		synctest.Wait()
		for i := range 8 { // 3 fit, 5 do not; Submit must return every time
			p.Submit(job(fmt.Sprint(i)))
		}
		if st := p.Stats(); st.Active != 1 || st.Queued != 3 || st.Rejected != 5 {
			t.Fatalf("stats: %+v", st)
		}
		// Retries have their own bound.
		for range 5 {
			p.Submit(Job{MonitorID: "r", Due: time.Now(), Retry: true})
		}
		if st := p.Stats(); st.Queued != 6 || st.Rejected != 7 {
			t.Fatalf("stats: %+v", st)
		}
		if n := strings.Count(logs.String(), "worker queue is full"); n != 1 {
			t.Fatalf("%d overload lines within a minute, want 1:\n%s", n, logs)
		}
		time.Sleep(warnEvery)
		p.Submit(job("later"))
		if n := strings.Count(logs.String(), "worker queue is full"); n != 2 {
			t.Fatalf("%d overload lines after a minute, want 2", n)
		}
		close(release)
		synctest.Wait()
		if ran.Load() != 7 { // 1 running + 3 regular + 3 retries
			t.Fatalf("ran %d", ran.Load())
		}
	})
}

func TestPoolCountsLateStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, logs := startPool(t, 1, 0, func(context.Context, Job) { time.Sleep(3 * time.Second) })
		p.Submit(job("on-time"))
		synctest.Wait()
		p.Submit(job("waits-3s"))
		p.Submit(job("waits-6s"))
		time.Sleep(10 * time.Second)
		synctest.Wait()
		st := p.Stats()
		if st.LateStarts != 2 || st.MaxLate != 6*time.Second {
			t.Fatalf("stats: %+v", st)
		}
		if !strings.Contains(logs.String(), "checks are starting late") {
			t.Fatalf("no late-start warning:\n%s", logs)
		}
		// Exactly at the limit is not late.
		p.Submit(Job{MonitorID: "edge", Due: time.Now().Add(-lateAfter)})
		time.Sleep(4 * time.Second)
		if got := p.Stats().LateStarts; got != 2 {
			t.Fatalf("LateStarts = %d", got)
		}
	})
}

func TestPoolCancelStopsChecksAndAbandonsQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Repeated because a worker coming back from a cancelled check sees
		// the cancellation and the waiting retries at once, and Go picks
		// between ready channels at random.
		for range 50 {
			var started, cancelled atomic.Int64
			p := NewPool(2, 0, func(ctx context.Context, _ Job) {
				started.Add(1)
				<-ctx.Done() // a check against a target that never answers
				cancelled.Add(1)
			}, slog.New(slog.DiscardHandler))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { p.Run(ctx); close(done) }()
			p.Submit(job("a"))
			p.Submit(job("b"))
			synctest.Wait() // both workers are in a check
			for i := range 6 {
				p.Submit(Job{MonitorID: fmt.Sprint(i), Due: time.Now(), Retry: i%2 == 0})
			}
			cancel()
			<-done
			if started.Load() != 2 || cancelled.Load() != 2 {
				t.Fatalf("started %d, cancelled %d; waiting jobs must not start after cancel", started.Load(), cancelled.Load())
			}
			p.Submit(job("after")) // still must not block
		}
	})
}

func TestPoolSurvivesAPanickingCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ran atomic.Int64
		p, logs := startPool(t, 1, 0, func(_ context.Context, j Job) {
			if j.MonitorID == "bad" {
				panic("boom")
			}
			ran.Add(1)
		})
		p.Submit(job("bad"))
		p.Submit(job("good"))
		synctest.Wait()
		if ran.Load() != 1 || p.Stats().Active != 0 {
			t.Fatalf("ran %d, stats %+v", ran.Load(), p.Stats())
		}
		if out := logs.String(); !strings.Contains(out, "check panicked") || !strings.Contains(out, "monitor_id=bad") {
			t.Fatalf("panic not logged:\n%s", out)
		}
	})
}

// 1,000 monitors due in the same instant all get checked, with no more
// goroutines than workers and nothing dropped.
func TestSchedulerFeedsPool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const n = 1000
		var mu sync.Mutex
		seen := map[string]int{}
		p, _ := startPool(t, 8, 0, func(_ context.Context, j Job) {
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			seen[j.MonitorID]++
			mu.Unlock()
		})
		sch := New(p.Submit)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { sch.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()
		for i := range n {
			sch.Set(fmt.Sprintf("monitor-%d", i), time.Hour, 0)
		}
		time.Sleep(n / 8 * 100 * time.Millisecond)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != n {
			t.Fatalf("%d of %d monitors checked", len(seen), n)
		}
		st := p.Stats()
		if st.Rejected != 0 || st.Queued != 0 || st.Active != 0 {
			t.Fatalf("stats: %+v", st)
		}
		// The last jobs waited 12.4 s for one of 8 workers: that is late.
		if st.LateStarts == 0 || st.MaxLate != (n/8-1)*100*time.Millisecond {
			t.Fatalf("late-start accounting: %+v", st)
		}
	})
}

// Workers that never finish must not stop the scheduler: it keeps handing
// out (and dropping) jobs and keeps applying commands.
func TestSchedulerNeverBlocksOnExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := startPool(t, 1, 2, func(ctx context.Context, _ Job) { <-ctx.Done() })
		sch := New(p.Submit)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { sch.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()
		sch.Set("0", 10*time.Second, 0)
		synctest.Wait() // the only worker is stuck on monitor 0
		for i := 1; i < 10; i++ {
			sch.Set(fmt.Sprint(i), 10*time.Second, 0)
		}
		synctest.Wait()
		// 1 stuck in the worker, 2 queued, 7 dropped.
		if st := p.Stats(); st.Active != 1 || st.Queued != 2 || st.Rejected != 7 {
			t.Fatalf("stats after the first round: %+v", st)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		// Five more rounds of ten jobs by now, all dropped: the queue is full.
		if st := p.Stats(); st.Rejected != 57 || st.Queued != 2 {
			t.Fatalf("the scheduler stopped dispatching: %+v", st)
		}
		sch.Remove("0")
		sch.Set("new", time.Minute, 0)
		synctest.Wait()
		if sch.Len() != 10 {
			t.Fatalf("commands are not applied: Len = %d", sch.Len())
		}
	})
}

// Real goroutines submitting while workers drain (meaningful under -race).
func TestPoolConcurrentSubmit(t *testing.T) {
	var ran atomic.Int64
	p := NewPool(8, 64, func(context.Context, Job) { ran.Add(1) }, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 500 {
				p.Submit(Job{MonitorID: fmt.Sprint(g, i), Due: time.Now(), Retry: i%5 == 0})
				p.Stats()
			}
		})
	}
	wg.Wait()
	// Every job either ran or was counted as dropped; none is lost.
	deadline := time.Now().Add(10 * time.Second)
	for ran.Load()+int64(p.Stats().Rejected) != 4000 {
		if time.Now().After(deadline) {
			t.Fatalf("ran %d + rejected %d, want 4000 in total", ran.Load(), p.Stats().Rejected)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
