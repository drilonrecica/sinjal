// Package dispatch delivers notifications (docs/11_NOTIFICATIONS.md
// "Dispatcher"). It takes the intents the result processor decided, routes
// each through the monitor's profile to its channels, and sends with a
// bounded number of attempts, leaving a row per attempt, the outcome on the
// incident's timeline and the channel's health behind.
//
// One goroutine (Run) owns every delivery and all database access; only the
// sends themselves run beside it, a bounded number at a time. Nothing here
// decides an intent: a failed notification is recorded, never announced.
package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

const (
	// queueSize is how many intents may wait to be routed. The processor
	// hands over at most a batch at a time and routing is a few reads, so
	// it only fills when the database stalls; then intents are dropped,
	// because the processor must not wait.
	queueSize = 1024
	// maxSending is how many deliveries may be on the network at once.
	maxSending = 4
	// maxWaiting is how many deliveries may wait for a free sender or for
	// their retry.
	maxWaiting = 4096
	// maxErrorRunes bounds the error kept for an attempt.
	maxErrorRunes = 300
)

// retryDelays is the wait before each further attempt, counted from the
// failure before it (docs/19 "Notification provider outage"): one attempt
// at once, then these, then the notification is given up.
var retryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// Dispatcher routes and delivers notification intents.
type Dispatcher struct {
	db  *db.DB
	key *vault.Key
	loc *time.Location // the instance time zone: quiet hours, times in messages
	log *slog.Logger
	// incidents is told that an incident's timeline got an entry, channels
	// that a channel's health changed. Neither may block.
	incidents func(incidentID, monitorID string)
	channels  func(channelID string)

	in chan incident.Intent

	// Replaced in tests.
	send   func(context.Context, notify.Config, notify.Message) error
	delays []time.Duration

	dropped atomic.Uint64 // intents and deliveries there was no room for
}

// New returns a dispatcher that is not running yet. Quiet hours are read
// and times are shown in loc (UTC when nil). incidents is called with an
// incident whose timeline got an entry, channels with a channel whose
// health changed (the SSE hub); either may be nil and neither may block.
func New(d *db.DB, key *vault.Key, loc *time.Location, incidents func(incidentID, monitorID string), channels func(channelID string), log *slog.Logger) *Dispatcher {
	if loc == nil {
		loc = time.UTC
	}
	if incidents == nil {
		incidents = func(string, string) {}
	}
	if channels == nil {
		channels = func(string) {}
	}
	return &Dispatcher{
		db: d, key: key, loc: loc, log: log,
		incidents: incidents, channels: channels,
		in:     make(chan incident.Intent, queueSize),
		send:   notify.Send,
		delays: retryDelays,
	}
}

// Enqueue hands an intent to the dispatcher. It never waits: this is the
// result processor's callback. A suppressed intent is not delivered (the
// processor has recorded it); an intent that finds the queue full is
// dropped and logged.
func (d *Dispatcher) Enqueue(in incident.Intent) {
	if in.Suppressed != "" {
		return
	}
	select {
	case d.in <- in:
	default:
		d.dropped.Add(1)
		d.log.Error("notification dropped: the queue is full", "kind", string(in.Kind),
			"monitor_id", in.MonitorID, "incident_id", in.IncidentID, "waiting", queueSize)
	}
}

// Stats is a snapshot for diagnostics.
type Stats struct {
	Queued  int    // intents waiting to be routed
	Dropped uint64 // intents and deliveries dropped for lack of room
}

// Stats returns the dispatcher's counters.
func (d *Dispatcher) Stats() Stats {
	return Stats{Queued: len(d.in), Dropped: d.dropped.Load()}
}

// delivery is one notification on its way to one channel.
type delivery struct {
	intent      incident.Intent
	channelID   string
	channelName string
	msg         notify.Message // rendered once; every attempt sends the same
	attempt     int            // attempts made so far
	due         time.Time      // when the next attempt may start
	lastErr     string         // why the last attempt failed
}

// outcome is a finished send.
type outcome struct {
	d   *delivery
	err error
}

// What attempt did with a delivery that was due.
const (
	started = iota // a send is running
	retry          // the attempt failed before any send; it waits again
	ended          // nothing more happens to it
)

// Run routes intents and delivers until ctx is cancelled. Sends that are
// running then are cut off and nothing that was waiting is delivered later:
// a notification is sent at most once, also across a restart (docs/19).
// Only the sends end with ctx; the database work of a step that has begun
// is finished, so the database must stay open until Run returns.
func (d *Dispatcher) Run(ctx context.Context) {
	var waiting []*delivery
	sending := 0
	done := make(chan outcome, maxSending) // never blocks a sender
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		// Start what is due, as far as senders are free.
		now := time.Now()
		var next time.Time
		keep := waiting[:0]
		for _, w := range waiting {
			if sending < maxSending && !w.due.After(now) {
				switch d.attempt(ctx, w, done) {
				case started:
					sending++
					continue
				case ended:
					continue
				}
			}
			keep = append(keep, w)
			if next.IsZero() || w.due.Before(next) {
				next = w.due
			}
		}
		clear(waiting[len(keep):])
		waiting = keep

		// With every sender busy, the next thing to happen is an outcome.
		var wake <-chan time.Time
		if sending < maxSending && !next.IsZero() {
			timer.Reset(max(time.Until(next), 0))
			wake = timer.C
		}

		select {
		case <-ctx.Done():
			// The senders end with ctx. One that got through in the last
			// moment is still recorded; the rest is not delivered later.
			lost := len(waiting) + len(d.in)
			for ; sending > 0; sending-- {
				if o := <-done; o.err == nil {
					d.settle(ctx, o)
				} else {
					lost++
				}
			}
			if lost > 0 {
				d.log.Warn("notifications not delivered before shutdown", "count", lost)
			}
			return
		case in := <-d.in:
			for _, w := range d.route(ctx, in) {
				if len(waiting) >= maxWaiting {
					d.dropped.Add(1)
					d.log.Error("notification dropped: too many are waiting", "kind", string(in.Kind),
						"monitor_id", in.MonitorID, "incident_id", in.IncidentID, "channel_id", w.channelID, "waiting", maxWaiting)
					continue
				}
				waiting = append(waiting, w)
			}
		case o := <-done:
			sending--
			if d.settle(ctx, o) {
				waiting = append(waiting, o.d)
			}
		case <-wake:
		}
	}
}

// logger carries what identifies an intent in every line about it.
func (d *Dispatcher) logger(in incident.Intent) *slog.Logger {
	return d.log.With("kind", string(in.Kind), "monitor_id", in.MonitorID, "incident_id", in.IncidentID)
}

// route decides where an intent goes: one delivery per enabled channel the
// monitor's profile routes the intent's severity to, unless the profile's
// quiet hours hold or this DOWN or RECOVERY was handed to delivery before.
func (d *Dispatcher) route(ctx context.Context, in incident.Intent) []*delivery {
	ctx = context.WithoutCancel(ctx)
	log := d.logger(in)
	t, err := store.GetNotificationTarget(ctx, d.db.Reader, in.MonitorID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // the monitor was deleted
	}
	if err != nil {
		log.Error("notification not sent: the monitor could not be read", "error", err)
		return nil
	}
	if t.ProfileID == "" {
		return nil
	}
	kind := notify.KindOf(in.Kind)
	severity := notify.SeverityOf(kind)
	if t.QuietEnabled && notify.InQuietHours(t.QuietStart, t.QuietEnd, in.At, d.loc) &&
		(severity != notify.SeverityCritical || !t.CriticalBypass) {
		d.quiet(ctx, in, log)
		return nil
	}
	channels, err := store.RouteChannels(ctx, d.db.Reader, t.ProfileID, string(severity))
	if err != nil {
		log.Error("notification not sent: the profile's routes could not be read", "error", err)
		return nil
	}
	if len(channels) == 0 {
		return nil
	}
	event, err := d.event(ctx, in, kind, t)
	if errors.Is(err, store.ErrNotFound) {
		return nil // the incident went with its monitor
	}
	if err != nil {
		log.Error("notification not sent: the incident could not be read", "error", err)
		return nil
	}
	if in.IncidentID != "" && (in.Kind == incident.IntentDown || in.Kind == incident.IntentRecovery) {
		claimed, err := store.ClaimNotification(ctx, d.db, in.IncidentID, in.Kind, time.Now())
		if err != nil {
			log.Error("notification not sent: it could not be recorded", "error", err)
			return nil
		}
		if !claimed {
			log.Warn("notification not sent again: this incident has had it")
			return nil
		}
	}
	msg := notify.Render(event, d.loc)
	now := time.Now()
	out := make([]*delivery, len(channels))
	for i, c := range channels {
		out[i] = &delivery{intent: in, channelID: c.ID, channelName: c.Name, msg: msg, due: now}
	}
	return out
}

// quiet records that the profile's quiet hours held an intent back. It is
// not sent later: quiet hours suppress, they do not postpone (docs/11).
func (d *Dispatcher) quiet(ctx context.Context, in incident.Intent, log *slog.Logger) {
	log.Info("notification not sent: quiet hours")
	if in.IncidentID == "" {
		return
	}
	message := string(in.Kind) + ": " + string(incident.ByQuietHours)
	if err := store.AddNotificationEvent(ctx, d.db, in.IncidentID, incident.EventNotificationSuppressed, message, in.At); err != nil {
		log.Error("the quiet-hours suppression could not be recorded", "error", err)
		return
	}
	d.incidents(in.IncidentID, in.MonitorID)
}

// event collects what the message of an intent says.
func (d *Dispatcher) event(ctx context.Context, in incident.Intent, kind notify.Kind, t store.NotificationTarget) (notify.Event, error) {
	e := notify.Event{
		Kind: kind, MonitorID: in.MonitorID, MonitorName: t.MonitorName, MonitorType: t.MonitorType,
		IncidentID: in.IncidentID, At: in.At,
	}
	if in.Kind == incident.IntentTLSWarning && t.TLSNotAfter != nil {
		e.CertExpiry = *t.TLSNotAfter
		e.DaysLeft = int(math.Floor(t.TLSNotAfter.Sub(in.At).Hours() / 24))
	}
	if in.IncidentID == "" {
		return e, nil
	}
	f, err := store.GetIncidentFacts(ctx, d.db.Reader, in.IncidentID)
	if err != nil {
		return e, err
	}
	e.IncidentStart = f.StartedAt
	var before time.Time
	switch in.Kind {
	case incident.IntentDown:
		// "Failed at" is the first failure, also for a DOWN that was held
		// back and is announced later; the latency is the last good one.
		e.At, e.Reason, e.Attempts = f.StartedAt, f.Summary, f.Attempts
		before = f.StartedAt
	case incident.IntentRecovery:
		end := in.At
		if f.EndedAt != nil {
			end = *f.EndedAt
		}
		e.Duration = end.Sub(f.StartedAt)
	case incident.IntentReminder:
		e.Duration, e.Reason = in.At.Sub(f.StartedAt), f.Summary
		return e, nil
	default:
		return e, nil
	}
	latency, ok, err := store.LastLatency(ctx, d.db.Reader, in.MonitorID, before)
	if err != nil {
		return e, err
	}
	if ok {
		e.Latency = &latency
	}
	return e, nil
}

// attempt starts the next attempt of a delivery that is due. The channel's
// configuration is read, and decrypted, for this attempt only.
func (d *Dispatcher) attempt(ctx context.Context, w *delivery, done chan<- outcome) int {
	log := d.logger(w.intent).With("channel_id", w.channelID)
	read := context.WithoutCancel(ctx)
	// A retry of an outage that is over would announce it after the fact
	// (docs/11 "Do not queue stale initial alerts"). The first attempt
	// always goes out.
	if w.attempt > 0 && w.intent.Kind == incident.IntentDown && w.intent.IncidentID != "" {
		over, err := store.IncidentEnded(read, d.db.Reader, w.intent.IncidentID)
		if err != nil {
			log.Error("the incident could not be read; sending anyway", "error", err)
		} else if over {
			d.drop(ctx, w, log)
			return ended
		}
	}
	c, cfg, err := store.GetChannel(read, d.db.Reader, d.key, w.channelID)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && !c.Enabled):
		log.Info("notification not sent: the channel was removed or disabled", "attempt", w.attempt+1)
		return ended
	case err != nil:
		// The stored error names no secret, but it is for the log; the
		// channel's row gets a sentence an owner can act on.
		log.Error("the channel's configuration could not be read", "error", err)
		w.attempt++
		if d.settle(ctx, outcome{d: w, err: errors.New("the channel's configuration cannot be read; save it again")}) {
			return retry
		}
		return ended
	}
	w.attempt++
	msg := w.msg
	go func() { done <- outcome{d: w, err: d.send(ctx, cfg, msg)} }()
	return started
}

// settle records how an attempt ended and reports whether the delivery
// waits for another one.
func (d *Dispatcher) settle(ctx context.Context, o outcome) (again bool) {
	w := o.d
	log := d.logger(w.intent).With("channel_id", w.channelID, "attempt", w.attempt)
	if o.err != nil && ctx.Err() != nil {
		// Cut off by shutdown: that says nothing about the channel.
		return false
	}
	now := time.Now()
	a := store.DeliveryAttempt{
		IncidentID: w.intent.IncidentID, ChannelID: w.channelID, EventType: string(w.intent.Kind),
		Attempt: w.attempt, At: now,
	}
	via := string(w.intent.Kind) + " via " + w.channelName
	switch {
	case o.err == nil:
		a.Status, a.Final, a.TimelineMessage = store.DeliverySent, true, via
		log.Info("notification sent")
	case w.attempt > len(d.delays):
		w.lastErr = errorText(o.err)
		a.Status, a.Final, a.Error = store.DeliveryFailed, true, w.lastErr
		a.TimelineMessage = via + ": " + w.lastErr
		log.Error("notification failed: no attempts left", "error", w.lastErr)
	default:
		w.lastErr = errorText(o.err)
		a.Status, a.Error = store.DeliveryFailed, w.lastErr
		wait := d.wait(w.attempt, o.err)
		w.due = now.Add(wait)
		log.Warn("notification attempt failed", "error", w.lastErr, "retry_in", wait.String())
	}
	d.record(ctx, a, w, log)
	return !a.Final
}

// drop ends a delivery whose incident ended before its retry.
func (d *Dispatcher) drop(ctx context.Context, w *delivery, log *slog.Logger) {
	log.Info("notification dropped: the incident ended before the retry", "attempt", w.attempt+1)
	reason := "not sent again, the incident had ended"
	d.record(ctx, store.DeliveryAttempt{
		IncidentID: w.intent.IncidentID, ChannelID: w.channelID, EventType: string(w.intent.Kind),
		Attempt: w.attempt + 1, Status: store.DeliveryDropped, At: time.Now(), Error: reason, Final: true,
		TimelineMessage: string(w.intent.Kind) + " via " + w.channelName + ": " + reason + " (last error: " + w.lastErr + ")",
	}, w, log)
}

// record stores an attempt and announces what it changed. A write that
// fails is logged; the delivery goes on from what happened on the network,
// never from what could be stored, so nothing is sent twice for it.
func (d *Dispatcher) record(ctx context.Context, a store.DeliveryAttempt, w *delivery, log *slog.Logger) {
	err := store.RecordDelivery(context.WithoutCancel(ctx), d.db, a)
	if errors.Is(err, store.ErrNotFound) {
		return // the channel was deleted meanwhile
	}
	if err != nil {
		log.Error("the delivery attempt could not be recorded", "status", a.Status, "error", err)
		return
	}
	if a.Status != store.DeliveryDropped {
		d.channels(w.channelID)
	}
	if a.Final && a.IncidentID != "" {
		d.incidents(a.IncidentID, w.intent.MonitorID)
	}
}

// wait is how long after its n-th failed attempt a delivery is tried
// again. A channel that asks for a longer pause gets it, up to the longest
// step, so the whole stays bounded.
func (d *Dispatcher) wait(n int, err error) time.Duration {
	step := d.delays[n-1]
	var limited *notify.RateLimitError
	if errors.As(err, &limited) {
		step = max(step, min(limited.After, d.delays[len(d.delays)-1]))
	}
	return step
}

// errorText is an error as it is stored: the senders already keep it to
// one line without secrets; this bounds its length.
func errorText(err error) string {
	if r := []rune(err.Error()); len(r) > maxErrorRunes {
		return string(r[:maxErrorRunes-1]) + "…"
	}
	return err.Error()
}
