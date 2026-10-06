// Package results is the single write path for check results
// (docs/09_DATABASE.md): workers hand results to one goroutine, which
// stores them in batched transactions, applies the state machine, updates
// the monitor rows, opens and closes incidents and decides which
// notifications they call for.
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
	"github.com/drilonrecica/sinjal/internal/maintenance"
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

// failure is what an incident keeps of a failed check.
type failure struct {
	at            time.Time
	kind, message string
}

// tracked is a monitor's counters and the state they were counted in.
type tracked struct {
	state    incident.State
	since    string
	counters incident.Counters
	// first is the failure that began the failures being counted: an
	// incident starts there, not at the check that confirms it.
	first failure
}

// Processor stores results and decides state changes.
type Processor struct {
	db     *db.DB
	log    *slog.Logger
	loc    *time.Location // the instance time zone, for maintenance windows
	retry  func(monitorID string, delay time.Duration)
	notify func(monitorID string)
	intent func(incident.Intent)
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

// New returns a processor writing to d. Maintenance windows repeat in loc,
// the instance time zone. retry is called when a monitor
// needs a confirmation check after the given delay (the scheduler's Retry),
// notify after a monitor's row changed (the SSE hub), intent for every
// notification intent once it is committed, suppressed ones included (the
// notification dispatcher); each may be nil. All are called from the
// processor's goroutine and must not block.
func New(d *db.DB, log *slog.Logger, loc *time.Location, retry func(monitorID string, delay time.Duration), notify func(monitorID string), intent func(incident.Intent)) *Processor {
	return &Processor{
		db:         d,
		log:        log,
		loc:        loc,
		retry:      retry,
		notify:     notify,
		intent:     intent,
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
	flapping   bool              // the FLAPPING overlay is set
	parentID   string            // the monitor this one depends on, if any
	intents    []incident.Intent // decided in this batch, in order
	// pending is the active incident whose DOWN notification the parent or
	// maintenance holds back; "" when there is none.
	pending string
	// windows are the maintenance windows covering the monitor, read the
	// first time this batch needs them.
	windows     []maintenance.Window
	windowsRead bool
	loc         *time.Location
	// lastTransition is when the monitor last went down or recovered. It
	// is only known, and only needed, while the monitor is flapping.
	lastTransition time.Time
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
			if err := apply(ctx, tx, w, r); err != nil {
				return err
			}
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
		for _, in := range w.intents {
			p.log.Info("notification intent", "kind", string(in.Kind), "monitor_id", id,
				"incident_id", in.IncidentID, "suppressed", string(in.Suppressed))
			if p.intent != nil {
				p.intent(in)
			}
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

// apply stores one result and what follows from it: the monitor's new
// state and, when the state machine confirms an outage or its end, the
// incident, the FLAPPING overlay and the intents to notify
// (docs/10_INCIDENTS.md). A monitor that stays down opens nothing, which is
// why a restart during an outage can neither duplicate its incident nor
// announce it again.
func apply(ctx context.Context, tx *sql.Tx, w *monitorWork, r *Result) error {
	if err := store.InsertCheckResult(ctx, tx, store.CheckResult{
		MonitorID: r.MonitorID, CheckedAt: r.CheckedAt, Duration: r.Duration, Success: r.Success,
		ProtocolStatus: r.Status, ErrorKind: r.Kind, ErrorMessage: r.Message, Snippet: r.Snippet, Metadata: r.Metadata,
	}); err != nil {
		return err
	}
	// Flapping ends FlapWindow after the last transition. That moment has
	// passed unnoticed, so it is settled first, for the state the monitor
	// was in, and this result is then an ordinary one.
	if w.flapping && incident.FlapEnded(w.lastTransition, stored(r.CheckedAt)) {
		if err := w.endFlapping(ctx, tx, r); err != nil {
			return err
		}
	}
	// A DOWN held back by the parent or by maintenance is decided once
	// neither holds any more and the monitor is still down.
	if w.pending != "" && w.state == incident.Down {
		c, err := w.conditions(ctx, tx, r)
		if err == nil && !c.ParentDown && !c.Maintenance {
			err = w.intend(ctx, tx, incident.IntentDown, r, w.pending)
		}
		if err != nil {
			return err
		}
	}
	out := incident.Transition(w.state, w.counters, r.Success, w.thresholds)
	changed := out.State != w.state
	if err := store.ApplyCheck(ctx, tx, r.MonitorID, store.CheckUpdate{
		State: string(out.State), StateChanged: changed, CheckedAt: r.CheckedAt,
		Success: r.Success, TLSNotAfter: r.TLSNotAfter,
	}); err != nil {
		return err
	}
	if !r.Success && w.state != incident.Down && w.counters.Failures == 0 {
		w.first = failure{at: r.CheckedAt, kind: r.Kind, message: r.Message}
	}
	switch {
	case changed && out.State == incident.Down:
		c, err := w.conditions(ctx, tx, r)
		if err != nil {
			return err
		}
		overlap, err := w.inMaintenance(ctx, tx, r.MonitorID, w.first.at, r.CheckedAt)
		if err != nil {
			return err
		}
		id, opened, err := store.OpenIncident(ctx, tx, store.NewIncident{
			MonitorID: r.MonitorID, StartedAt: w.first.at, DeclaredAt: r.CheckedAt,
			FailureKind: w.first.kind, Detected: w.first.message, Summary: r.Message,
			SuppressedByParent: c.ParentDown, MaintenanceOverlap: overlap,
		})
		if err == nil && opened {
			err = w.transition(ctx, tx, incident.IntentDown, r, id, w.first.at)
		}
		if err != nil {
			return err
		}
	case changed && w.state == incident.Down:
		id, closed, err := store.CloseIncident(ctx, tx, r.MonitorID, r.CheckedAt, incident.EventRecovered)
		if err == nil && closed {
			err = w.markOverlap(ctx, tx, r, id)
		}
		if err == nil && closed {
			err = w.transition(ctx, tx, incident.IntentRecovery, r, id, r.CheckedAt)
		}
		if err != nil {
			return err
		}
	}
	if changed {
		w.since = store.FormatTime(r.CheckedAt)
	}
	w.state, w.counters, w.retry = out.State, out.Counters, out.Retry
	return nil
}

// stored is t as the database keeps it: in whole seconds. Flapping is
// decided on these values, so that it does not depend on whether a time
// was still in memory or has been read back after a restart.
func stored(t time.Time) time.Time { return t.Truncate(time.Second) }

// transition follows an incident that result r opened or closed: the
// transition counts towards flapping at the given time (an opening at the
// incident's start, as a restart will read it back), the overlay is set if
// it is the one too many, and the intent of the given kind is decided,
// which the overlay then suppresses.
func (w *monitorWork) transition(ctx context.Context, tx *sql.Tx, kind incident.IntentKind, r *Result, incidentID string, at time.Time) error {
	w.lastTransition = stored(at)
	if !w.flapping {
		recent, err := store.RecentTransitions(ctx, tx, r.MonitorID)
		if err != nil {
			return err
		}
		if incident.FlapStarts(recent, stored(r.CheckedAt)) {
			if err := store.SetFlapping(ctx, tx, r.MonitorID, &r.CheckedAt); err != nil {
				return err
			}
			w.flapping = true
			if err := w.intend(ctx, tx, incident.IntentFlapping, r, incidentID); err != nil {
				return err
			}
		}
	}
	return w.intend(ctx, tx, kind, r, incidentID)
}

// endFlapping clears the overlay and decides the one notification that
// ends it: DOWN, about the active incident, if the monitor is down, and
// STABLE otherwise.
func (w *monitorWork) endFlapping(ctx context.Context, tx *sql.Tx, r *Result) error {
	if err := store.SetFlapping(ctx, tx, r.MonitorID, nil); err != nil {
		return err
	}
	w.flapping = false
	if w.state != incident.Down {
		return w.intend(ctx, tx, incident.IntentStable, r, "")
	}
	id, err := store.ActiveIncidentID(ctx, tx, r.MonitorID)
	if err != nil {
		return err
	}
	return w.intend(ctx, tx, incident.IntentDown, r, id)
}

// conditions is what may hold back the monitor's notifications at the time
// of result r. The parent is read inside the batch, so a parent that
// changed earlier in it counts; parent and maintenance windows are read
// only when an intent is being decided.
func (w *monitorWork) conditions(ctx context.Context, tx *sql.Tx, r *Result) (incident.Conditions, error) {
	c := incident.Conditions{Flapping: w.flapping}
	var err error
	if w.parentID != "" {
		if c.ParentDown, err = store.ParentDown(ctx, tx, w.parentID); err != nil {
			return c, err
		}
	}
	if err = w.readWindows(ctx, tx, r.MonitorID); err != nil {
		return c, err
	}
	for _, mw := range w.windows {
		if mw.Suppress && mw.Active(r.CheckedAt, w.loc) {
			c.Maintenance = true
		}
	}
	return c, nil
}

// readWindows reads the maintenance windows covering the monitor, once per
// batch.
func (w *monitorWork) readWindows(ctx context.Context, tx *sql.Tx, monitorID string) error {
	if w.windowsRead {
		return nil
	}
	var err error
	w.windows, err = store.MonitorWindows(ctx, tx, monitorID)
	w.windowsRead = err == nil
	return err
}

// inMaintenance reports whether a maintenance window covering the monitor,
// suppressing or not, is in effect at some time from from to to.
func (w *monitorWork) inMaintenance(ctx context.Context, tx *sql.Tx, monitorID string, from, to time.Time) (bool, error) {
	if err := w.readWindows(ctx, tx, monitorID); err != nil {
		return false, err
	}
	for _, mw := range w.windows {
		if len(mw.Occurrences(from, to.Add(time.Second), w.loc)) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// markOverlap sets maintenance_overlap on an incident that result r closed
// if a window was in effect at any time during it.
func (w *monitorWork) markOverlap(ctx context.Context, tx *sql.Tx, r *Result, incidentID string) error {
	if err := w.readWindows(ctx, tx, r.MonitorID); err != nil || len(w.windows) == 0 {
		return err
	}
	started, err := store.IncidentStart(ctx, tx, incidentID)
	if err != nil {
		return err
	}
	overlap, err := w.inMaintenance(ctx, tx, r.MonitorID, started, r.CheckedAt)
	if err != nil || !overlap {
		return err
	}
	return store.SetMaintenanceOverlap(ctx, tx, incidentID)
}

// intend decides a notification intent for the monitor at the time of
// result r. A suppressed intent about an incident is recorded on that
// incident's timeline, so the decision can be read back later; so is a
// DOWN that was held back and is now decided after all.
func (w *monitorWork) intend(ctx context.Context, tx *sql.Tx, kind incident.IntentKind, r *Result, incidentID string) error {
	c, err := w.conditions(ctx, tx, r)
	if err != nil {
		return err
	}
	in := incident.Intent{Kind: kind, MonitorID: r.MonitorID, IncidentID: incidentID, At: r.CheckedAt,
		Suppressed: incident.Suppression(kind, c)}
	if in.Suppressed != "" && incidentID != "" {
		if err := store.AddIncidentEvent(ctx, tx, incidentID, incident.EventNotificationSuppressed,
			string(kind)+": "+string(in.Suppressed), r.CheckedAt); err != nil {
			return err
		}
	}
	if kind == incident.IntentDown && incidentID != "" {
		switch in.Suppressed {
		case incident.ByParent, incident.ByMaintenance:
			w.pending = incidentID
		case "":
			if w.pending == incidentID {
				if err := store.AddIncidentEvent(ctx, tx, incidentID, incident.EventNotificationResumed,
					"", r.CheckedAt); err != nil {
					return err
				}
			}
			w.pending = ""
		default:
			w.pending = ""
		}
	}
	w.intents = append(w.intents, in)
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
		flapping:   cs.Flapping,
		parentID:   cs.ParentID,
		loc:        p.loc,
	}
	if prev, ok := p.counters[id]; ok && prev.state == w.state && prev.since == w.since {
		w.tracked = prev
	}
	if w.state == incident.Down && !w.skip {
		if w.pending, err = store.PendingDown(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	if w.flapping && !w.skip {
		recent, err := store.RecentTransitions(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if len(recent) > 0 {
			w.lastTransition = recent[0]
		}
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
