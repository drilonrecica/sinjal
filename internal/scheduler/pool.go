package scheduler

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultQueue is how many jobs may wait for a worker, for retries and
	// for regular jobs each. One job per monitor fits with room to spare at
	// the tested scale of 1,000 monitors.
	DefaultQueue = 1024
	// lateAfter is how long a job may wait past its due time before its
	// start counts as late.
	lateAfter = time.Second
	// warnEvery limits the overload log lines.
	warnEvery = time.Minute
)

// Pool runs jobs on a fixed number of workers. The goroutine count is the
// worker count, whatever the number of monitors.
type Pool struct {
	run     func(context.Context, Job)
	log     *slog.Logger
	workers int
	urgent  chan Job // retries and run-now: taken first
	normal  chan Job

	active     atomic.Int64
	rejected   atomic.Uint64
	lateStarts atomic.Uint64
	maxLate    atomic.Int64
	lastWarn   atomic.Int64
}

// PoolStats is a snapshot for diagnostics.
type PoolStats struct {
	Workers    int
	Active     int           // jobs running now
	Queued     int           // jobs waiting for a worker
	Rejected   uint64        // jobs dropped because the queue was full
	LateStarts uint64        // jobs started more than a second after they were due
	MaxLate    time.Duration // the latest start so far
}

// NewPool returns a pool of workers that call run for each job. queue is
// the number of waiting jobs allowed (DefaultQueue when not positive). run
// gets the pool's context and must return soon after it is cancelled.
func NewPool(workers, queue int, run func(context.Context, Job), log *slog.Logger) *Pool {
	if queue < 1 {
		queue = DefaultQueue
	}
	return &Pool{
		run:     run,
		log:     log,
		workers: max(workers, 1),
		urgent:  make(chan Job, queue),
		normal:  make(chan Job, queue),
	}
}

// Submit queues a job and never blocks. When the queue is full the job is
// dropped and counted: the monitor is simply checked again at its next
// interval, and memory use stays bounded however far the pool is behind.
func (p *Pool) Submit(j Job) {
	q := p.normal
	if j.Retry {
		q = p.urgent
	}
	select {
	case q <- j:
	default:
		p.rejected.Add(1)
		p.warn("worker queue is full, checks are being skipped", "skipped_total", p.rejected.Load())
	}
}

// Run starts the workers and returns when ctx is cancelled and all of them
// have stopped. Checks in flight are cancelled through their context; jobs
// still waiting are abandoned.
func (p *Pool) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range p.workers {
		wg.Go(func() { p.work(ctx) })
	}
	wg.Wait()
}

func (p *Pool) work(ctx context.Context) {
	for {
		var j Job
		select {
		case <-ctx.Done():
			return
		case j = <-p.urgent:
		default:
			select {
			case <-ctx.Done():
				return
			case j = <-p.urgent:
			case j = <-p.normal:
			}
		}
		if ctx.Err() != nil {
			return
		}
		if late := time.Since(j.Due); late > lateAfter {
			p.lateStarts.Add(1)
			for {
				old := p.maxLate.Load()
				if int64(late) <= old || p.maxLate.CompareAndSwap(old, int64(late)) {
					break
				}
			}
			p.warn("checks are starting late, all workers are busy", "late", late.Round(time.Millisecond), "queued", len(p.urgent)+len(p.normal))
		}
		p.execute(ctx, j)
	}
}

// execute runs one job. A panic in a check must not take the worker, and
// with it all monitoring, down: it is logged and the worker carries on.
func (p *Pool) execute(ctx context.Context, j Job) {
	p.active.Add(1)
	defer func() {
		p.active.Add(-1)
		if r := recover(); r != nil {
			p.log.Error("check panicked", "monitor_id", j.MonitorID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	p.run(ctx, j)
}

// warn logs at most once per warnEvery; overload repeats every interval.
func (p *Pool) warn(msg string, args ...any) {
	now := time.Now().UnixNano()
	last := p.lastWarn.Load()
	if last != 0 && now-last < int64(warnEvery) {
		return
	}
	if p.lastWarn.CompareAndSwap(last, now) {
		p.log.Warn(msg, args...)
	}
}

// Stats returns the pool's counters.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Workers:    p.workers,
		Active:     int(p.active.Load()),
		Queued:     len(p.urgent) + len(p.normal),
		Rejected:   p.rejected.Load(),
		LateStarts: p.lateStarts.Load(),
		MaxLate:    time.Duration(p.maxLate.Load()),
	}
}
