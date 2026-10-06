package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// recorder collects dispatched jobs with the (virtual) time they arrived.
type recorder struct {
	mu   sync.Mutex
	jobs []Job
	at   []time.Time
}

func (r *recorder) dispatch(j Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, j)
	r.at = append(r.at, time.Now())
}

// take returns what was dispatched since the last call, as "id@seconds"
// (seconds since start, "!" for a retry).
func (r *recorder) take(start time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := ""
	for i, j := range r.jobs {
		mark := ""
		if j.Retry {
			mark = "!"
		}
		out += fmt.Sprintf("%s%s@%g ", j.MonitorID, mark, r.at[i].Sub(start).Seconds())
	}
	r.jobs, r.at = nil, nil
	return out
}

// start runs a scheduler inside a synctest bubble.
func start(t *testing.T) (*Scheduler, *recorder, time.Time) {
	t.Helper()
	rec := &recorder{}
	sch := New(rec.dispatch)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sch, rec, time.Now()
}

func expect(t *testing.T, rec *recorder, begin time.Time, want string) {
	t.Helper()
	synctest.Wait()
	if got := rec.take(begin); got != want {
		t.Fatalf("dispatched %q, want %q", got, want)
	}
}

func TestSchedulerRunsOnTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		const iv = 30 * time.Second
		off := jitter("m", iv).Seconds()
		sch.Set("m", iv, 0)
		expect(t, rec, begin, "m@0 ")
		if sch.Len() != 1 {
			t.Fatalf("Len = %d", sch.Len())
		}
		time.Sleep(iv + jitter("m", iv) - time.Nanosecond)
		expect(t, rec, begin, "")
		time.Sleep(time.Nanosecond)
		expect(t, rec, begin, fmt.Sprintf("m@%g ", 30+off))
		time.Sleep(3 * iv)
		expect(t, rec, begin, fmt.Sprintf("m@%g m@%g m@%g ", 60+off, 90+off, 120+off))
	})
}

func TestSchedulerFirstCheckDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("a", time.Minute, 2*time.Second)
		sch.Set("b", time.Minute, time.Second)
		time.Sleep(time.Second)
		expect(t, rec, begin, "b@1 ")
		time.Sleep(time.Second)
		expect(t, rec, begin, "a@2 ")
	})
}

func TestSchedulerEditTakesEffectPromptly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("m", time.Hour, 0)
		expect(t, rec, begin, "m@0 ")
		time.Sleep(10 * time.Second)
		// The interval drops from an hour to 20 s: checked now, not in 59 min.
		sch.Set("m", 20*time.Second, 0)
		expect(t, rec, begin, "m@10 ")
		time.Sleep(20*time.Second + jitter("m", 20*time.Second))
		expect(t, rec, begin, fmt.Sprintf("m@%g ", 30+jitter("m", 20*time.Second).Seconds()))
		if sch.Len() != 1 {
			t.Fatalf("Len = %d", sch.Len())
		}
	})
}

func TestSchedulerRemove(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("m", 10*time.Second, 0)
		sch.Set("n", 10*time.Second, 5*time.Second)
		expect(t, rec, begin, "m@0 ")
		sch.Remove("m")
		sch.Remove("n")
		synctest.Wait()
		if sch.Len() != 0 {
			t.Fatalf("Len = %d", sch.Len())
		}
		time.Sleep(time.Hour)
		expect(t, rec, begin, "")
		// Resume is Set.
		sch.Set("m", 10*time.Second, 0)
		expect(t, rec, begin, "m@3600 ")
	})
}

func TestSchedulerRunNowKeepsTheSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("m", time.Minute, 30*time.Second)
		sch.RunNow("unknown")
		expect(t, rec, begin, "")
		time.Sleep(10 * time.Second)
		sch.RunNow("m")
		expect(t, rec, begin, "m!@10 ")
		time.Sleep(20 * time.Second)
		expect(t, rec, begin, "m@30 ")
	})
}

func TestSchedulerRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("m", time.Minute, 0)
		expect(t, rec, begin, "m@0 ")
		time.Sleep(time.Second) // the check took a second and failed
		sch.Retry("m", 5*time.Second)
		time.Sleep(5*time.Second - time.Nanosecond)
		expect(t, rec, begin, "")
		time.Sleep(time.Nanosecond)
		expect(t, rec, begin, "m!@6 ")
		// One retry only, then the regular cadence.
		off := jitter("m", time.Minute)
		time.Sleep(54*time.Second + off)
		expect(t, rec, begin, fmt.Sprintf("m@%g ", 60+off.Seconds()))
		// A retry that would come after the next regular run is not added.
		sch.Retry("m", 2*time.Minute)
		time.Sleep(time.Minute)
		expect(t, rec, begin, fmt.Sprintf("m@%g ", 120+off.Seconds()))
		time.Sleep(time.Minute)
		expect(t, rec, begin, fmt.Sprintf("m@%g ", 180+off.Seconds()))
	})
}

func TestSchedulerRetryBeforeRegularJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		sch.Set("a", time.Hour, 10*time.Second)
		sch.Set("b", time.Hour, 10*time.Second)
		sch.Set("r", time.Hour, time.Minute)
		sch.Retry("r", 10*time.Second)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		got := rec.take(begin)
		if len(got) < 6 || got[:6] != "r!@10 " {
			t.Fatalf("dispatched %q, the retry should be first", got)
		}
	})
}

func TestSchedulerManyMonitorsSpreadOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sch, rec, begin := start(t)
		const n, iv = 1000, 30 * time.Second
		for i := range n {
			sch.Set(fmt.Sprintf("monitor-%d", i), iv, 0)
		}
		synctest.Wait()
		rec.take(begin)
		// All were added in the same instant; the second round must not
		// fire in one instant again.
		time.Sleep(iv + maxJitter(iv))
		synctest.Wait()
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if len(rec.jobs) != n {
			t.Fatalf("second round ran %d of %d", len(rec.jobs), n)
		}
		perSecond := map[int]int{}
		for _, at := range rec.at {
			d := at.Sub(begin)
			if d < iv || d > iv+maxJitter(iv) {
				t.Fatalf("job at %v, outside the jitter window", d)
			}
			perSecond[int(d/time.Second)]++
		}
		for sec, c := range perSecond {
			if c > n/2 {
				t.Errorf("%d of %d monitors fired in second %d", c, n, sec)
			}
		}
	})
}

func TestSchedulerStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		sch := New(rec.dispatch)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { sch.Run(ctx); close(done) }()
		sch.Set("m", 10*time.Second, 0)
		synctest.Wait()
		cancel()
		<-done
		rec.take(time.Now())
		// Nothing runs and no caller hangs once it has stopped.
		sch.Set("m", 10*time.Second, 0)
		sch.RunNow("m")
		sch.Retry("m", 0)
		sch.Remove("m")
		time.Sleep(time.Hour)
		if got := rec.take(time.Now()); got != "" {
			t.Fatalf("dispatched after stop: %q", got)
		}
	})
}

// Commands from many goroutines while jobs fire (meaningful under -race).
func TestSchedulerConcurrentCommands(t *testing.T) {
	var mu sync.Mutex
	count := 0
	sch := New(func(Job) { mu.Lock(); count++; mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				id := fmt.Sprintf("%d-%d", g, i%10)
				sch.Set(id, time.Second, 0)
				sch.RunNow(id)
				sch.Retry(id, 0)
				if i%3 == 0 {
					sch.Remove(id)
				}
				sch.Len()
			}
		}()
	}
	wg.Wait()
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if count == 0 {
		t.Fatal("nothing was dispatched")
	}
}
