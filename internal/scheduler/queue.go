// Package scheduler decides when monitors are checked and runs the checks
// on a bounded worker pool (docs/07_SCHEDULER.md). It knows monitor ids and
// intervals only; what a check does is the caller's business.
package scheduler

import (
	"container/heap"
	"time"
)

// Job is one check to run.
type Job struct {
	MonitorID string
	// Due is when the job should have started. It carries the monotonic
	// clock reading, so time.Since(Due) is how late a worker picked it up.
	Due time.Time
	// Retry marks a confirmation retry or a run-now request. These run
	// before regular jobs.
	Retry bool
}

// minInterval keeps a nonsensical interval from making a monitor due
// forever. Validation never lets one through (the minimum is 10 s).
const minInterval = time.Second

// item is one scheduled monitor.
type item struct {
	id       string
	interval time.Duration
	regular  time.Time // next run on the monitor's fixed cadence
	at       time.Time // heap key: regular, or an earlier retry / run-now
	extra    bool      // at is a retry or run-now, not the regular run
	phased   bool      // the one-time jitter offset has been applied
	index    int
}

// queue is a min-heap of monitors by next run, with an index by id. It has
// no clock and no goroutines: every method is told the current time.
type queue struct {
	items []*item
	byID  map[string]*item
}

func (q *queue) Len() int { return len(q.items) }

// Less orders by time; at the same instant a retry goes first.
func (q *queue) Less(i, j int) bool {
	a, b := q.items[i], q.items[j]
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.extra && !b.extra
}

func (q *queue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].index, q.items[j].index = i, j
}

func (q *queue) Push(x any) {
	it := x.(*item)
	it.index = len(q.items)
	q.items = append(q.items, it)
}

func (q *queue) Pop() any {
	n := len(q.items) - 1
	it := q.items[n]
	q.items[n] = nil
	q.items = q.items[:n]
	return it
}

// set schedules a monitor, new or already known: the first run is delay
// from now and later runs follow every interval. An existing schedule is
// replaced, so an edit takes effect at once.
func (q *queue) set(now time.Time, id string, interval, delay time.Duration) {
	first := now.Add(max(delay, 0))
	it := q.byID[id]
	if it == nil {
		if q.byID == nil {
			q.byID = make(map[string]*item)
		}
		it = &item{id: id}
		q.byID[id] = it
		heap.Push(q, it)
	}
	it.interval = max(interval, minInterval)
	it.regular, it.at = first, first
	it.extra, it.phased = false, false
	heap.Fix(q, it.index)
}

// remove takes a monitor off the queue (deleted, paused or disabled).
func (q *queue) remove(id string) {
	it := q.byID[id]
	if it == nil {
		return
	}
	heap.Remove(q, it.index)
	delete(q.byID, id)
}

// extraAt adds one run at t if that is earlier than the monitor's next run.
// The regular cadence does not move.
func (q *queue) extraAt(id string, t time.Time) {
	it := q.byID[id]
	if it == nil || !t.Before(it.at) {
		return
	}
	it.at, it.extra = t, true
	heap.Fix(q, it.index)
}

// next is the time of the earliest run.
func (q *queue) next() (time.Time, bool) {
	if len(q.items) == 0 {
		return time.Time{}, false
	}
	return q.items[0].at, true
}

// popDue returns the earliest job if it is due and moves its monitor to
// its next run.
func (q *queue) popDue(now time.Time) (Job, bool) {
	if len(q.items) == 0 || q.items[0].at.After(now) {
		return Job{}, false
	}
	it := q.items[0]
	job := Job{MonitorID: it.id, Due: it.at, Retry: it.extra}
	if it.extra {
		it.extra = false
	} else {
		it.regular = it.regular.Add(it.interval)
		if !it.phased {
			it.regular = it.regular.Add(jitter(it.id, it.interval))
			it.phased = true
		}
		if !it.regular.After(now) {
			// More than a whole interval behind (process stalled, machine
			// suspended): skip the missed runs instead of firing them all.
			it.regular = now.Add(it.interval)
		}
	}
	it.at = it.regular
	heap.Fix(q, it.index)
	return job, true
}
