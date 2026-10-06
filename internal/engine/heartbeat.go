package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/store"
)

// KindHeartbeatMissed is the failure kind of a heartbeat monitor whose
// deadline has passed without a beat (docs/30).
const KindHeartbeatMissed = "heartbeat_missed"

// ErrUnknownToken means a heartbeat token matches no monitor.
var ErrUnknownToken = errors.New("engine: unknown heartbeat token")

// A heartbeat monitor is not checked; its job runs at its deadline, last
// beat plus interval plus grace, and then every such period. Each beat
// moves the job back to a period from now, so the job only finds a monitor
// late when no beat came in time (docs/07 "Heartbeat monitors").

// setSchedule puts a monitor on the schedule: the first check after delay,
// or for a heartbeat monitor at its deadline (but not before delay). A
// stored deadline may be up to a second early, so the job waits a second
// longer; the job itself compares with the early one, so it never skips a
// deadline that has passed.
func (e *Engine) setSchedule(s store.Schedule, delay time.Duration) {
	if !s.Deadline.IsZero() {
		delay = max(delay, time.Until(s.Deadline)+time.Second)
	}
	e.sch.Set(s.ID, s.Interval, delay)
}

// heartbeat is the "check" of a heartbeat monitor that its job runs: a
// failure when the deadline has passed, nothing to store otherwise (a beat
// came in between and moved the job).
func (e *Engine) heartbeat(ctx context.Context, m store.Monitor, now time.Time) (results.Result, bool) {
	c, err := store.GetHeartbeatConfig(ctx, e.db.Reader, m.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return unusable(m.ID, now, "the heartbeat settings are missing"), true
	case err != nil:
		e.readFailed(ctx, m.ID, err)
		return results.Result{}, false
	}
	if now.Before(c.Deadline()) {
		return results.Result{}, false
	}
	return results.Result{
		MonitorID: m.ID,
		CheckedAt: now,
		Kind:      KindHeartbeatMissed,
		Message:   missedMessage(c),
	}, true
}

func missedMessage(c store.HeartbeatConfig) string {
	since := "no heartbeat received yet"
	if c.LastBeatAt != nil {
		since = "no heartbeat since " + c.LastBeatAt.UTC().Format(time.RFC3339)
	}
	if c.Grace == 0 {
		return fmt.Sprintf("%s (expected every %s)", since, c.ExpectedInterval)
	}
	return fmt.Sprintf("%s (expected every %s, grace %s)", since, c.ExpectedInterval, c.Grace)
}

// Beat records a heartbeat for the monitor with this token: the beat time
// is stored, the monitor gets a successful result and its deadline moves a
// period ahead. A paused monitor's beat is recorded but changes nothing
// else. ErrUnknownToken when the token belongs to no monitor. No payload
// is stored (docs/06).
func (e *Engine) Beat(ctx context.Context, token string) error {
	hash, ok := store.HashHeartbeatToken(token)
	if !ok {
		return ErrUnknownToken
	}
	now := time.Now()
	id, err := store.RecordBeat(ctx, e.db, hash, now)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnknownToken
	}
	if err != nil {
		return err
	}
	active, err := e.reschedule(ctx, id)
	if err != nil || !active {
		return err
	}
	// Outside the mutex: Add waits while the processor's queue is full,
	// and a pause must not wait for that.
	if !e.proc.Add(ctx, results.Result{MonitorID: id, CheckedAt: now, Success: true}) {
		return ctx.Err()
	}
	return nil
}

// reschedule moves a heartbeat monitor's job a period ahead after a beat,
// under the pause mutex so a beat racing a pause cannot schedule a paused
// monitor. It reports false for a paused, disabled or deleted monitor.
func (e *Engine) reschedule(ctx context.Context, id string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := store.GetMonitor(ctx, e.db.Reader, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !m.Enabled || m.State == string(incident.Paused) || m.Type != "heartbeat" {
		return false, nil
	}
	c, err := store.GetHeartbeatConfig(ctx, e.db.Reader, id)
	if err != nil {
		return false, err
	}
	e.sch.Set(id, c.Period(), c.Period())
	return true, nil
}
