# Scheduler and Concurrency

## Design

Use a central scheduler with a min-heap / priority queue ordered by next-run monotonic deadline.

Concept:

```text
monitor/config changes
        |
        v
scheduler command channel
        |
        v
priority queue
        |
        v
timer until next due monitor
        |
        v
bounded worker pool
        |
        v
result channel
        |
        v
result processor / DB writer
```

## Requirements

- no polling the entire DB every second
- no permanent goroutine per monitor
- deterministic shutdown
- monitor edits update scheduling promptly
- paused/disabled monitors are removed from active queue
- jitter recurring check schedules slightly
- timeout via `context.Context`
- scheduler itself must not block on check execution

## Scheduler commands

`internal/scheduler` keeps one heap entry per scheduled monitor: its id, its interval and its next run. It knows nothing about protocols or configuration.

Everything reaches it through one command channel, applied by the scheduler goroutine:

- `Set(id, interval, delay)`: add or update. First check after `delay`, then every `interval`. Calling it for a monitor that is already scheduled replaces its schedule, so an edit shows its effect at once.
- `Remove(id)`: stop checking. Used for delete, pause and disable alike; the scheduler keeps nothing for a paused monitor, and resume is `Set`.
- `RunNow(id)`: one immediate check ahead of regular jobs. The regular schedule does not change.
- `Retry(id, delay)`: one confirmation check, see "Retry execution".

The scheduler hands a due job to the worker pool without waiting. A single timer is armed for the earliest run; nothing polls.

A monitor's runs follow a fixed cadence (`previous due time + interval`), not `finish time + interval`, so a slow check does not stretch the interval. If the process falls a whole interval or more behind (stall, suspended machine), the missed runs are skipped: one check runs, and the next is one interval later.

## Worker pool

Default concurrency:
- automatically chosen
- suggested starting heuristic: `min(32, max(8, NumCPU*4))`

Expose override only under advanced/system settings or env configuration.

Do not make normal users tune worker count.

Implementation (`scheduler.Pool`):

- worker count: `SINJAL_WORKERS`, default `min(32, max(8, NumCPU*4))` (`15_CONFIG_BACKUP.md`); the pool starts exactly that many goroutines and no others
- two bounded queues of 1,024 jobs each: retries (and run-now) and regular jobs; a free worker always takes a retry first
- handing a job to the pool never blocks the scheduler

Overload (more checks due than the workers can run):

- a job that finds its queue full is dropped and counted (`Rejected`); the monitor is checked again at its next interval, so memory stays bounded however far the pool is behind
- a job that starts more than 1 s after it was due is counted as a late start (`LateStarts`, with the worst delay in `MaxLate`)
- each condition logs one WARN per minute at most
- the counters, with active workers and queue depth, are available from `Pool.Stats()` for Settings → System

A panic inside a check is logged with the monitor id and does not stop the worker.

## Jitter

Goal:
avoid all monitors firing on the same second.

Use small bounded jitter relative to interval, for example:
- up to a few seconds for 30–60s intervals
- proportionally bounded for longer intervals

Jitter must not make the configured interval misleading.

Implementation:

- each monitor has one fixed offset: `hash(monitor id) mod (min(interval / 10, 30 s) + 1 ns)`, FNV-1a, so the same after every restart and edit
- that is at most 1 s for a 10 s interval, 3 s for 30 s, 6 s for 60 s, and 30 s from 5 minutes up
- the first check is not delayed; the offset is added once, between the first and the second check
- from then on consecutive checks are exactly one interval apart: the offset shifts the monitor's phase, it does not vary from run to run

## Retry execution

A confirmation retry after a failure is higher priority than waiting for the normal next interval.

Default:
- failure
- wait 5s
- retry
- if failure -> DOWN

Do not recursively schedule unlimited retries.

Implementation:

- the result processor asks for a retry when the state machine says so (`10_INCIDENTS.md`): at most failure threshold − 1 in a row
- the retry is an extra run; the regular cadence is not moved
- if the next regular check is due before the retry would be, no extra run is added: that check is the confirmation
- when several jobs are due at the same instant, retries are handed out first, and the worker pool takes them before regular jobs

## State across restart

Persist enough monitor state to restore:
- current state
- current state since
- active incident
- last check
- last success/failure

On restart:
- do not re-send DOWN alerts for an already active incident
- schedule a fresh check promptly
- continue incident duration from persisted start time

## Clock behavior

Persist UTC wall-clock timestamps.

Use Go monotonic time for:
- in-process scheduling
- timeout/duration math

DST must have no effect on check intervals.

## Shutdown

On SIGTERM/SIGINT:
1. stop accepting new HTTP work where appropriate
2. stop adding new monitor jobs
3. cancel active checks
4. flush pending result writes
5. close DB cleanly
6. exit within a bounded grace period

Do not hang indefinitely waiting for a bad network target.
