package results

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/scheduler"
	"github.com/drilonrecica/sinjal/internal/store"
)

// The real scheduler, worker pool and processor together, on a real
// database, with a scripted check instead of HTTP. It covers scenarios 1-3
// of docs/20 at state level:
//
//	outage:  failure, retry fails, later success -> pending, down, up
//	blip:    failure, retry succeeds             -> pending, up
//	healthy: success                             -> up
func TestPipelineScenarios(t *testing.T) {
	d, _ := testDB(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	outage, blip, healthy := newMonitor(t, d, "outage", nil), newMonitor(t, d, "blip", nil), newMonitor(t, d, "healthy", nil)
	scripts := map[string][]bool{
		outage:  {false, false, true},
		blip:    {false, true},
		healthy: {true},
	}

	var mu sync.Mutex
	step := map[string]int{}      // checks run per monitor
	retried := map[string][]int{} // which of them were retries
	states := map[string][]string{}
	added := 0

	var proc *Processor
	pool := scheduler.NewPool(4, 0, func(ctx context.Context, j scheduler.Job) {
		mu.Lock()
		script := scripts[j.MonitorID]
		i := step[j.MonitorID]
		step[j.MonitorID]++
		if j.Retry {
			retried[j.MonitorID] = append(retried[j.MonitorID], i)
		}
		mu.Unlock()
		r := Result{MonitorID: j.MonitorID, CheckedAt: time.Now(), Duration: time.Millisecond, Success: script[min(i, len(script)-1)]}
		if !r.Success {
			r.Kind, r.Message = "connect", "connection refused"
		}
		if proc.Add(ctx, r) {
			mu.Lock()
			added++
			mu.Unlock()
		}
	}, quiet)
	sch := scheduler.New(pool.Submit)
	proc = New(d, quiet, sch.Retry, func(id string) {
		m, err := store.GetMonitor(context.Background(), d.Reader, id)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if s := states[id]; len(s) == 0 || s[len(s)-1] != m.State {
			states[id] = append(s, m.State)
		}
	}, nil)
	proc.flushAfter = 5 * time.Millisecond

	workCtx, stopWork := context.WithCancel(context.Background())
	procCtx, stopProc := context.WithCancel(context.Background())
	var workers, processor sync.WaitGroup
	workers.Go(func() { sch.Run(workCtx) })
	workers.Go(func() { pool.Run(workCtx) })
	processor.Go(func() { proc.Run(procCtx) })
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		// The order M2-13 will use: scheduler and workers first, then the
		// processor, so the last results are still stored.
		stopWork()
		workers.Wait()
		stopProc()
		processor.Wait()
	}
	defer stop()

	// One second is the scheduler's shortest interval; the retry delay of
	// 20 ms comes from the monitor rows.
	for id := range scripts {
		sch.Set(id, time.Second, 0)
	}

	want := map[string][]string{
		outage:  {"pending", "down", "up"},
		blip:    {"pending", "up"},
		healthy: {"up"},
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		done := reflect.DeepEqual(states, want)
		got := fmt.Sprint(states)
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("states seen: %s\nwant: %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()

	mu.Lock()
	defer mu.Unlock()
	// The second check of the two failing monitors was the confirmation
	// retry, well before their one-second interval; the healthy one had none.
	if !reflect.DeepEqual(retried[outage], []int{1}) || !reflect.DeepEqual(retried[blip], []int{1}) || len(retried[healthy]) != 0 {
		t.Fatalf("retries: %v", retried)
	}
	var rows int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if st := proc.Stats(); rows != added || st.Persisted != uint64(added) || st.Discarded != 0 || st.Warning != "" {
		t.Fatalf("%d results added, %d rows, stats %+v", added, rows, st)
	}
	for id, w := range want {
		m, err := store.GetMonitor(context.Background(), d.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.State != w[len(w)-1] || m.LastCheckAt == nil {
			t.Fatalf("%s: %+v", m.Name, m)
		}
	}
	if st := pool.Stats(); st.Rejected != 0 || st.Active != 0 {
		t.Fatalf("pool stats: %+v", st)
	}
}
