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

Not persisted: how many consecutive failures (or, while DOWN, successes) a monitor has collected towards its threshold. These counts live in the result processor's memory. After a restart a monitor that was part-way through a confirmation needs its full threshold again, which can delay a DOWN or a recovery by the checks already counted, never bring one forward. The same reset happens when a monitor is paused and resumed.

Implementation (`internal/engine`, started by `serve` once the listener is open):

- `Engine.Start` reads the enabled monitors (id and interval only) and schedules each one. Nothing else is restored into memory: state, state-since and the last check, success and failure are in the monitor row, and the result processor reads the row for every batch, so a monitor that was DOWN stays DOWN since the original moment until a check says otherwise
- the first checks after a start are 20 ms apart in id order, or closer together when there are more than 500 monitors, so that all of them have started within 10 s. The per-monitor jitter (above) then shifts each monitor's second check as usual
- if the monitors cannot be read, startup fails

## Running a check

The engine gives the worker pool one function. For each job it:

- reads the monitor, its HTTP configuration and its secrets from the reader pool: three indexed reads per check. Nothing is cached, so an edit or a new secret applies to the next check without any invalidation, and decrypted secrets do not stay in memory between checks
- skips the check when the monitor has been deleted or paused while the job was waiting
- skips the check, with an ERROR log, when the database cannot be read: that says nothing about the target
- stores a failed check (kind `unknown`, message "the monitor's configuration cannot be used: …") when the stored configuration cannot be turned into a request or a stored secret cannot be decrypted. Validation prevents this on save; it means the row or the master key was changed outside Sinjal, and it must be visible rather than leave the monitor unchecked
- runs the check and hands the result to the result processor

## Pause and resume

`Engine.Pause` and `Engine.Resume` are the only way a monitor is paused or resumed (`10_INCIDENTS.md` "Pausing"):

- pause: the row is changed first (`store.PauseMonitor`), then the monitor is removed from the scheduler. A job that was already waiting for a worker is skipped; the result of a check that was already running is discarded by the result processor
- resume: the row is changed (`store.ResumeMonitor`), then the monitor is scheduled with an immediate first check
- both do nothing when the monitor is already in that state, and the two steps of each are serialised, so concurrent calls cannot leave a monitor pending but unscheduled
- a pause or resume that changed the monitor is announced as `monitor.updated` (`32_SSE_EVENTS.md`), like every stored check result

## Create, edit and delete

After the monitor pages have written a new or edited monitor (and its secrets), they call `Engine.Schedule(id)`. Under the same mutex as pause and resume it reads the row: an enabled monitor that is not paused is `Set` with an immediate first check, so an edit applies at once; a disabled, paused or missing monitor is `Remove`d. Reading the row under the mutex is what keeps an edit racing a pause from scheduling a paused monitor.

`Engine.Delete(id)` deletes the row (with config, secrets and history, by cascade) and then `Remove`s the schedule, under the same mutex. A check that was running is discarded by the result processor, which no longer finds the monitor. `Pause` and `Resume` report whether they changed anything, so the handlers audit only real changes.

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

Implementation: the HTTP server and the engine stop on the same signal. In the engine the scheduler and the workers stop first, and only then the result processor, which writes what is still queued once more (`09_DATABASE.md`); `serve` waits for it (`Engine.Wait`) before the database closes.

A check that is cut off by shutdown is not stored. Its "failure" is no verdict about the target, and with a failure threshold of 1 it would mark the monitor DOWN on every restart. Results of checks that had finished are stored.
