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

## Worker pool

Default concurrency:
- automatically chosen
- suggested starting heuristic: `min(32, max(8, NumCPU*4))`

Expose override only under advanced/system settings or env configuration.

Do not make normal users tune worker count.

## Jitter

Goal:
avoid all monitors firing on the same second.

Use small bounded jitter relative to interval, for example:
- up to a few seconds for 30–60s intervals
- proportionally bounded for longer intervals

Jitter must not make the configured interval misleading.

## Retry execution

A confirmation retry after a failure is higher priority than waiting for the normal next interval.

Default:
- failure
- wait 5s
- retry
- if failure -> DOWN

Do not recursively schedule unlimited retries.

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
