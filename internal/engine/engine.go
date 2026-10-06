// Package engine runs the monitoring pipeline: it schedules the stored
// monitors, executes their checks on the worker pool and hands the results
// to the result processor (docs/07_SCHEDULER.md). It is the one place that
// knows both the store and the check executors; the scheduler stays free of
// protocols and the store free of scheduling.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/scheduler"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

const (
	// At startup the first checks are startStep apart, so a restart does
	// not open every connection in the same instant, and closer together
	// when that would take longer than startWindow.
	startStep   = 20 * time.Millisecond
	startWindow = 10 * time.Second
)

// Engine owns the scheduler, the worker pool, the result processor and the
// HTTP transports.
type Engine struct {
	db   *db.DB
	key  *vault.Key
	log  *slog.Logger
	http *httpcheck.Pool
	sch  *scheduler.Scheduler
	pool *scheduler.Pool
	proc *results.Processor

	// mu makes a pause or resume one step: the database change and the
	// scheduler command belong together, or two callers could leave a
	// monitor pending but unscheduled.
	mu   sync.Mutex
	done chan struct{} // closed when everything has stopped
}

// New returns an engine that is not running yet. workers is the number of
// checks that may run at once; userAgent is sent by HTTP checks that set
// none of their own.
func New(d *db.DB, key *vault.Key, workers int, userAgent string, logger *slog.Logger) *Engine {
	e := &Engine{
		db:   d,
		key:  key,
		log:  logging.Sub(logger, "engine"),
		http: httpcheck.NewPool(userAgent),
		done: make(chan struct{}),
	}
	e.pool = scheduler.NewPool(workers, 0, e.check, logging.Sub(logger, "scheduler"))
	e.sch = scheduler.New(e.pool.Submit)
	e.proc = results.New(d, logging.Sub(logger, "results"), e.sch.Retry, nil)
	return e
}

// Start schedules every enabled monitor and returns; checks run until ctx
// is cancelled. Each monitor continues from the state stored in its row and
// gets a fresh check promptly, the first checks spread over a short window.
// An error means the monitors could not be read and nothing was started.
// Call it once, and Wait after cancelling ctx.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	schedules, err := store.ListSchedules(ctx, e.db.Reader)
	if err != nil {
		return err
	}

	procCtx, stopProc := context.WithCancel(context.Background())
	var work, processor sync.WaitGroup
	work.Go(func() { e.sch.Run(ctx) })
	work.Go(func() { e.pool.Run(ctx) })
	processor.Go(func() { e.proc.Run(procCtx) })
	go func() {
		defer close(e.done)
		// The scheduler and the workers first, then the processor, so the
		// results that were finished in time are still stored.
		work.Wait()
		stopProc()
		processor.Wait()
		e.http.Close()
	}()

	for i, s := range schedules {
		e.sch.Set(s.ID, s.Interval, startDelay(i, len(schedules)))
	}
	e.log.Info("monitoring started", "monitors", len(schedules))
	return nil
}

// Wait returns when the scheduler, the workers and the result processor
// have stopped, which they do once the context given to Start is cancelled.
func (e *Engine) Wait() { <-e.done }

// startDelay is how long the i-th of n monitors waits for its first check
// after a start.
func startDelay(i, n int) time.Duration {
	return time.Duration(i) * min(startStep, startWindow/time.Duration(n))
}

// Pause stops checking a monitor and marks it paused (docs/10 "Pausing").
// A check that is running at that moment is discarded by the result
// processor. Pausing a paused monitor does nothing.
func (e *Engine) Pause(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := store.PauseMonitor(ctx, e.db, id, time.Now()); err != nil {
		return err
	}
	e.sch.Remove(id)
	return nil
}

// Resume makes a paused monitor pending and checks it at once. Resuming a
// monitor that is not paused does nothing.
func (e *Engine) Resume(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed, err := store.ResumeMonitor(ctx, e.db, id, time.Now())
	if err != nil || !changed {
		return err
	}
	m, err := store.GetMonitor(ctx, e.db.Reader, id)
	if err != nil {
		return err
	}
	e.sch.Set(id, time.Duration(m.IntervalSeconds)*time.Second, 0)
	return nil
}

// check runs one job on a worker and hands its result to the processor.
func (e *Engine) check(ctx context.Context, j scheduler.Job) {
	res, ok := e.run(ctx, j.MonitorID)
	// A check cut off by shutdown is not a verdict about the target: with
	// a failure threshold of one, storing it would mark the monitor DOWN
	// on every restart.
	if !ok || ctx.Err() != nil {
		return
	}
	e.proc.Add(ctx, res)
}

// run executes the check of one monitor. Its configuration and secrets are
// read for every check, so an edit applies to the next check without any
// invalidation and decrypted secrets do not stay in memory. It reports
// false when there is nothing to store: the monitor is gone or paused, or
// the database could not be read, which says nothing about the target.
func (e *Engine) run(ctx context.Context, id string) (results.Result, bool) {
	started := time.Now()
	m, err := store.GetMonitor(ctx, e.db.Reader, id)
	if err == nil && m.State == string(incident.Paused) {
		return results.Result{}, false
	}
	var c store.HTTPConfig
	if err == nil {
		c, err = store.GetHTTPConfig(ctx, e.db.Reader, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return results.Result{}, false // deleted while the job was waiting
	}
	if err != nil {
		e.readFailed(ctx, id, err)
		return results.Result{}, false
	}
	secrets, err := store.Secrets(ctx, e.db.Reader, e.key, id)
	switch {
	case errors.Is(err, vault.ErrDecrypt), errors.Is(err, vault.ErrMalformed), errors.Is(err, vault.ErrUnknownVersion):
		return unusable(id, started, "a stored secret cannot be decrypted; enter it again"), true
	case err != nil:
		e.readFailed(ctx, id, err)
		return results.Result{}, false
	}
	cfg, err := buildHTTPConfig(m, c, secrets)
	if err != nil {
		return unusable(id, started, err.Error()), true
	}
	return toResult(id, c.TLSExpiryEnabled, e.http.Check(ctx, cfg)), true
}

func (e *Engine) readFailed(ctx context.Context, id string, err error) {
	if ctx.Err() == nil {
		e.log.Error("check skipped: the monitor could not be read", "monitor_id", id, "error", err)
	}
}

// unusable is the failed check of a monitor whose stored configuration
// cannot be turned into a request. It is stored like any failure, so the
// problem is visible instead of the monitor silently going unchecked.
func unusable(id string, started time.Time, reason string) results.Result {
	return results.Result{
		MonitorID: id,
		CheckedAt: started,
		Duration:  time.Since(started),
		Kind:      httpcheck.KindUnknown,
		Message:   "the monitor's configuration cannot be used: " + reason,
	}
}
