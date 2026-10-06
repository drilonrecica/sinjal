package scheduler

import (
	"context"
	"sync/atomic"
	"time"
)

type op uint8

const (
	opSet op = iota
	opRemove
	opRunNow
	opRetry
)

type command struct {
	op       op
	id       string
	interval time.Duration
	delay    time.Duration
}

// Scheduler owns the queue. One goroutine (Run) applies commands and hands
// due jobs to dispatch; there is no goroutine per monitor and nothing polls.
type Scheduler struct {
	dispatch func(Job)
	cmds     chan command
	done     chan struct{} // closed when Run returns
	size     atomic.Int64
}

// New returns a scheduler that passes due jobs to dispatch. dispatch is
// called on the scheduler's goroutine and must not block: it hands the job
// to the worker pool or drops it.
func New(dispatch func(Job)) *Scheduler {
	return &Scheduler{
		dispatch: dispatch,
		cmds:     make(chan command),
		done:     make(chan struct{}),
	}
}

// Run schedules until ctx is cancelled. Call it once, on its own goroutine;
// commands wait until it is running and are dropped after it has returned.
func (s *Scheduler) Run(ctx context.Context) {
	defer close(s.done)
	var q queue
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		now := time.Now()
		for {
			job, ok := q.popDue(now)
			if !ok {
				break
			}
			s.dispatch(job)
		}
		s.size.Store(int64(q.Len()))
		if next, ok := q.next(); ok {
			timer.Reset(next.Sub(now))
		} else {
			timer.Stop()
		}
		select {
		case <-ctx.Done():
			return
		case c := <-s.cmds:
			now = time.Now()
			switch c.op {
			case opSet:
				q.set(now, c.id, c.interval, c.delay)
			case opRemove:
				q.remove(c.id)
			case opRunNow:
				q.extraAt(c.id, now)
			case opRetry:
				q.extraAt(c.id, now.Add(max(c.delay, 0)))
			}
		case <-timer.C:
		}
	}
}

func (s *Scheduler) send(c command) {
	select {
	case s.cmds <- c:
	case <-s.done:
	}
}

// Set schedules a monitor: first check after delay, then every interval.
// It is both "add" and "update": a monitor that is already scheduled gets
// the new interval and a fresh first check, so an edit shows its effect
// promptly.
func (s *Scheduler) Set(id string, interval, delay time.Duration) {
	s.send(command{op: opSet, id: id, interval: interval, delay: delay})
}

// Remove stops checking a monitor: deleted, paused or disabled. Resuming
// is Set.
func (s *Scheduler) Remove(id string) {
	s.send(command{op: opRemove, id: id})
}

// RunNow checks a scheduled monitor immediately, ahead of regular jobs.
// Its regular schedule does not change. Unknown ids are ignored.
func (s *Scheduler) RunNow(id string) {
	s.send(command{op: opRunNow, id: id})
}

// Retry asks for one confirmation check after delay, ahead of regular
// jobs. If the monitor's next regular check comes first, that check is the
// confirmation and no extra one is added.
func (s *Scheduler) Retry(id string, delay time.Duration) {
	s.send(command{op: opRetry, id: id, delay: delay})
}

// Len is the number of scheduled monitors.
func (s *Scheduler) Len() int { return int(s.size.Load()) }
