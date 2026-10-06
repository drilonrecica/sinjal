// Package results is the single write path for check results
// (docs/09_DATABASE.md): workers hand results to one goroutine, which
// stores them in batched transactions, applies the state machine and
// updates the monitor rows.
package results

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/store"
)

const (
	// queueSize is how many results may wait for the processor before
	// workers block.
	queueSize = 256
	// A batch is written when it holds batchMax results or flushAfter has
	// passed since its first one, whichever comes first.
	batchMax   = 128
	flushAfter = 200 * time.Millisecond
	// retryAfter is the pause before a batch that could not be written is
	// tried again.
	retryAfter = time.Second
	// flushTimeout bounds one write, busy retries included; finalTimeout
	// bounds the last write during shutdown.
	flushTimeout = 30 * time.Second
	finalTimeout = 5 * time.Second
	// failureLogEvery limits the log lines while writes keep failing.
	failureLogEvery = time.Minute
)

// Result is the outcome of one check, whatever the monitor type.
type Result struct {
	MonitorID   string
	CheckedAt   time.Time // when the check started
	Duration    time.Duration
	Success     bool
	Status      string // protocol status such as "503"; "" when there is none
	Kind        string // failure kind; "" on success
	Message     string
	Snippet     string
	Metadata    string     // metadata_json; "" when there is none
	TLSNotAfter *time.Time // certificate expiry seen by the check, if any
}

// Stats is a snapshot for diagnostics.
type Stats struct {
	Queued        int    // results waiting for the processor
	Persisted     uint64 // results stored
	Discarded     uint64 // late results of paused or deleted monitors
	FailedFlushes uint64 // batch writes that failed
	// Warning is set while results cannot be written and cleared by the
	// next successful write. The results concerned are held, not lost.
	Warning string
}

// tracked is a monitor's counters and the state they were counted in.
type tracked struct {
	state    incident.State
	since    string
	counters incident.Counters
}

// Processor stores results and decides state changes.
type Processor struct {
	db     *db.DB
	log    *slog.Logger
	retry  func(monitorID string, delay time.Duration)
	notify func(monitorID string)
	in     chan Result

	// Tunable in tests.
	batchMax   int
	flushAfter time.Duration
	retryAfter time.Duration

	// counters holds the consecutive failures or successes of monitors
	// that are part-way to a state change. Only Run touches it. It is not
	// persisted: after a restart a monitor needs its full threshold again.
	counters map[string]tracked

	persisted, discarded, failedFlushes atomic.Uint64
	warning                             atomic.Pointer[string]
	lastFailureLog                      time.Time
}

// New returns a processor writing to d. retry is called when a monitor
// needs a confirmation check after the given delay (the scheduler's Retry),
// notify after a monitor's row changed (the SSE hub); either may be nil.
// Both are called from the processor's goroutine and must not block.
func New(d *db.DB, log *slog.Logger, retry func(monitorID string, delay time.Duration), notify func(monitorID string)) *Processor {
	return &Processor{
		db:         d,
		log:        log,
		retry:      retry,
		notify:     notify,
		in:         make(chan Result, queueSize),
		batchMax:   batchMax,
		flushAfter: flushAfter,
		retryAfter: retryAfter,
		counters:   make(map[string]tracked),
	}
}

// Add hands a result to the processor. It waits while the queue is full,
// which is how a database that cannot keep up slows the workers down
// instead of losing results, and reports false if ctx ends first.
func (p *Processor) Add(ctx context.Context, r Result) bool {
	select {
	case p.in <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

// Run processes results until ctx is cancelled, then stores what is still
// queued and returns. Cancel it only after the workers have stopped, so
// nothing is added behind the final write.
func (p *Processor) Run(ctx context.Context) {
	batch := make([]Result, 0, p.batchMax)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	flush := func() {
		wctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		err := p.write(wctx, batch)
		cancel()
		if err != nil {
			p.failed(err, len(batch))
			timer.Reset(p.retryAfter)
			return
		}
		batch = batch[:0]
		timer.Stop()
	}

	for {
		in := p.in
		if len(batch) >= p.batchMax {
			// A full batch that could not be written is being held: take
			// nothing more until it is stored. The queue fills, workers
			// wait in Add, and memory stays bounded.
			in = nil
		}
		select {
		case <-ctx.Done():
			for {
				select {
				case r := <-p.in:
					batch = append(batch, r)
					continue
				default:
				}
				break
			}
			if len(batch) == 0 {
				return
			}
			wctx, cancel := context.WithTimeout(context.Background(), finalTimeout)
			defer cancel()
			if err := p.write(wctx, batch); err != nil {
				p.failedFlushes.Add(1)
				p.log.Error("check results lost at shutdown: they could not be saved", "results", len(batch), "error", err)
			}
			return
		case r := <-in:
			batch = append(batch, r)
			if len(batch) >= p.batchMax {
				flush()
			} else if len(batch) == 1 {
				timer.Reset(p.flushAfter)
			}
		case <-timer.C:
			flush()
		}
	}
}

// failed raises the system warning for a batch that could not be written.
// The batch stays in memory and is retried; nothing is dropped.
func (p *Processor) failed(err error, held int) {
	msg := "check results cannot be saved and are being held: " + err.Error()
	var busy *db.BusyExhaustedError
	if errors.As(err, &busy) {
		msg = "the database stayed busy; check results are being held: " + err.Error()
	}
	// The warning before the counter, so a failed write is never visible
	// without its warning.
	p.warning.Store(&msg)
	p.failedFlushes.Add(1)
	if now := time.Now(); now.Sub(p.lastFailureLog) >= failureLogEvery {
		p.lastFailureLog = now
		p.log.Error("check results could not be saved, will retry", "held", held, "failed_writes", p.failedFlushes.Load(), "error", err)
	}
}

// monitorWork is one monitor's progress through a batch.
type monitorWork struct {
	tracked
	skip       bool // deleted or paused: its results are discarded
	thresholds incident.Thresholds
	retryDelay time.Duration
	retry      bool // the last result asks for a confirmation retry
	from       incident.State
}

// write stores a batch in one transaction and then applies its effects.
func (p *Processor) write(ctx context.Context, batch []Result) error {
	var work map[string]*monitorWork
	var stored, discarded int

	err := db.Retry(ctx, func() error {
		// Retry runs this again from the top on SQLITE_BUSY, so everything
		// decided here stays local until the commit has succeeded.
		work = make(map[string]*monitorWork, len(batch))
		stored, discarded = 0, 0
		tx, err := p.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		for i := range batch {
			r := &batch[i]
			w := work[r.MonitorID]
			if w == nil {
				if w, err = p.load(ctx, tx, r.MonitorID); err != nil {
					return err
				}
				work[r.MonitorID] = w
			}
			if w.skip {
				discarded++
				continue
			}
			if err := store.InsertCheckResult(ctx, tx, store.CheckResult{
				MonitorID: r.MonitorID, CheckedAt: r.CheckedAt, Duration: r.Duration, Success: r.Success,
				ProtocolStatus: r.Status, ErrorKind: r.Kind, ErrorMessage: r.Message, Snippet: r.Snippet, Metadata: r.Metadata,
			}); err != nil {
				return err
			}
			out := incident.Transition(w.state, w.counters, r.Success, w.thresholds)
			changed := out.State != w.state
			if err := store.ApplyCheck(ctx, tx, r.MonitorID, store.CheckUpdate{
				State: string(out.State), StateChanged: changed, CheckedAt: r.CheckedAt,
				Success: r.Success, TLSNotAfter: r.TLSNotAfter,
			}); err != nil {
				return err
			}
			if changed {
				w.since = store.FormatTime(r.CheckedAt)
			}
			w.state, w.counters, w.retry = out.State, out.Counters, out.Retry
			stored++
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}

	if p.warning.Swap(nil) != nil {
		p.lastFailureLog = time.Time{}
		p.log.Info("check results are being saved again")
	}
	for id, w := range work {
		if w.skip || w.counters == (incident.Counters{}) {
			delete(p.counters, id)
		} else {
			p.counters[id] = w.tracked
		}
		if w.skip {
			continue
		}
		if w.state != w.from {
			p.log.Info("monitor state changed", "monitor_id", id, "from", string(w.from), "to", string(w.state))
		}
		if w.retry && p.retry != nil {
			p.retry(id, w.retryDelay)
		}
		if p.notify != nil {
			p.notify(id)
		}
	}
	// Published last: once the counters move, the batch is fully applied.
	p.persisted.Add(uint64(stored))
	p.discarded.Add(uint64(discarded))
	return nil
}

// load starts a monitor's work for this batch from its row and from the
// counters kept in memory. The counters only apply if the row is still in
// the state, since the same moment, that they were counted in: a pause and
// resume, or any other change behind the processor's back, starts the
// count again.
func (p *Processor) load(ctx context.Context, tx *sql.Tx, id string) (*monitorWork, error) {
	cs, err := store.GetCheckState(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return &monitorWork{skip: true}, nil
	}
	if err != nil {
		return nil, err
	}
	w := &monitorWork{
		tracked:    tracked{state: incident.State(cs.State), since: cs.StateSince},
		skip:       cs.State == string(incident.Paused),
		thresholds: incident.Thresholds{Failure: cs.FailureThreshold, Success: cs.SuccessThreshold},
		retryDelay: cs.RetryDelay,
		from:       incident.State(cs.State),
	}
	if prev, ok := p.counters[id]; ok && prev.state == w.state && prev.since == w.since {
		w.counters = prev.counters
	}
	return w, nil
}

// Stats returns the processor's counters.
func (p *Processor) Stats() Stats {
	s := Stats{
		Queued:        len(p.in),
		Persisted:     p.persisted.Load(),
		Discarded:     p.discarded.Load(),
		FailedFlushes: p.failedFlushes.Load(),
	}
	if w := p.warning.Load(); w != nil {
		s.Warning = *w
	}
	return s
}
