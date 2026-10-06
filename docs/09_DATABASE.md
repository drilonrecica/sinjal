# Database and Storage

## SQLite

Use SQLite with `modernc.org/sqlite`.

Required:
- WAL mode
- foreign keys on
- busy timeout
- sensible synchronous mode
- indexes based on actual query paths

Suggested startup pragmas should be benchmarked and documented. A reasonable baseline:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
```

Do not blindly add exotic pragmas.

Connections (`internal/db`):
- one writer handle with a single connection (`MaxOpenConns=1`): the only write path
- a reader pool (`max(4, NumCPU)`) opened with `query_only=1`, so it cannot write
- pragmas are applied to every connection through the DSN

## Write architecture

Workers do not independently hammer SQLite.

Use:
- buffered result channel
- one result-processing path
- batched transactions for check-result inserts where useful

The result processor is responsible for:
- persisting check result
- applying state transition
- opening/closing incident
- enqueueing notification intents
- emitting SSE update

Keep transaction boundaries clear.

Implementation (`internal/results`, SQL in `internal/store/results.go`):

- workers call `Processor.Add`; a buffered channel of 256 results feeds one goroutine
- a batch is written when it holds 128 results or 200 ms after its first result, whichever comes first; nothing wakes up while no result is waiting
- one transaction per batch. For each result, in arrival order: read the monitor's state and thresholds, insert the `check_results` row, apply the state machine (`10_INCIDENTS.md`), update the monitor row (`current_state`, `current_state_since` when the state changed, `last_check_at`, `last_success_at` or `last_failure_at`, `tls_not_after`)
- when the state becomes DOWN the incident and its `detected` and `declared_down` events are inserted; when it leaves DOWN the incident is closed with a `recovered` event (`10_INCIDENTS.md`). Results that do not change the state cost no extra statement
- `tls_not_after` is replaced when the check saw a certificate, cleared when a check succeeded without one, and kept on a failed check that saw none
- opening or closing an incident also decides the notification intent (`down`, `recovery`) and whether it is suppressed; a suppressed one is recorded as an incident event in the same transaction (`10_INCIDENTS.md` "Notification intents")
- an incident opening or closing is also a flapping transition: the monitor's last incidents are read and `flapping_since` is set when it is the fourth within 10 minutes; a flapping monitor's overlay is cleared by the first result 10 minutes after its last transition (`10_INCIDENTS.md` "Flapping")
- deciding an intent reads the parent's state when the monitor has a parent (one indexed read, inside the batch); a DOWN monitor costs one read per batch to know whether its DOWN is held back (`10_INCIDENTS.md` "Parent dependency")
- only after the commit: the confirmation retry is requested from the scheduler, the monitor is announced for SSE (`monitor.updated`, once per monitor per batch; `32_SSE_EVENTS.md`) and the batch's intents are logged and handed on
- a result for a monitor that has been deleted or paused in the meantime (a check that was already running) is discarded and counted, not stored

## Busy handling

Transient `SQLITE_BUSY`:
- bounded retry/backoff
- example: 25ms, 100ms, 250ms, 1s
- after limit, surface a system warning/error

Do not silently drop results.

When a batch cannot be written (busy through every retry, or any other error):

- the batch is kept in memory and tried again after 1 s, until it is stored
- an ERROR is logged (at most once a minute while it lasts) and `Processor.Stats().Warning` is set; it is cleared by the next successful write
- once the held batch is full, the processor stops taking results: the channel fills, workers wait, the worker queue fills and the scheduler skips checks (`07_SCHEDULER.md`). Memory stays bounded and no stored history is invented or lost silently
- at shutdown the queued results are written once more within 5 s; if that fails, the number of lost results is logged

`db.Retry` implements this: it retries only `SQLITE_BUSY`, waits 25 ms, 100 ms, 250 ms, then 1 s, and after the last retry returns a `*db.BusyExhaustedError` that callers must surface. The function passed to `Retry` is re-run in full, so it must contain a complete transaction.

## Retention jobs

Daily internal job:
1. roll raw records older than 7d into 5m buckets
2. roll 5m buckets older than 30d into 1h buckets
3. roll 1h buckets older than 365d into 1d buckets
4. delete source rows after successful rollup transaction
5. conditionally optimize/vacuum only if justified

Do not VACUUM every day by default.

## History queries

Ranges (`internal/history.Parse`): presets 1 hour, 24 hours (default), 7, 30 and 90 days and 1 year, ending now; day presets are calendar days in the instance time zone. A custom range is `from`/`to` as local date and time; an end in the future becomes now; it must end after it starts, start in the past and span at most 400 days, otherwise the default preset is shown with the reason.

`store.LatencyHistory(monitor, from, to, buckets)` reads the range once along `idx_check_results_monitor_time` (in index order, no sort) and returns:

- the summary: checks, failures, samples, current (newest sample in the range), min, average, max and exact p95 (nearest rank, the sample at rank ⌈0.95 n⌉), over successful checks with a duration; a failure's duration is how long it took to fail and is not latency
- the series: the range in at most `buckets` equal buckets of whole seconds, only those holding results, each with checks, failures, average and max latency; the chart asks for 720

One pass in Go is faster than SQL aggregates plus an `ORDER BY` for the percentile plus a `GROUP BY` for the buckets (21 ms instead of 85 ms for a week of 30 s results, `18_PERFORMANCE.md`). Its cost grows with the raw rows in range, so ranges beyond raw retention are served from the rollups once M6 adds them (M6-05), with approximate p95.

## p95

For raw ranges, calculate exact p95 from raw samples.

For long aggregated ranges, p95 may be approximate unless the implementation stores a compact percentile sketch. Do not add a heavy sketch dependency unless benchmarks justify it.

The UI should not imply SLA-grade percentile precision for rolled-up historical data.

Bucket math (`internal/history/bucket.go`):

- buckets are aligned to the Unix epoch, so in UTC; a daily bucket is a UTC day, and every bucket of a resolution has the same length across DST changes
- a 5-minute bucket is built from raw results (`FromRaw`): total, success and failure counts, and min, max, average and **exact** p95 (nearest rank, the rule of raw history) over the successful checks; a bucket of failures only has no latency (NULL columns)
- an hourly or daily bucket is built from the finer buckets it covers (`Merge`): counts summed, the smallest minimum, the largest maximum, the average weighted by successful checks (every stored result has a duration, so `success_count` is the bucket's sample count). Counts, extremes and average are therefore exact at every tier
- its p95 is **approximate** (`ApproxP95`): the weighted nearest rank over the children's p95 values, each weighted by its successful checks, i.e. the smallest child p95 at which the cumulative weight reaches ⌈0.95 × total⌉. With all weights 1 it is the exact nearest rank, so raw samples and rolled buckets can be combined by the same function. Over buckets it is an estimate that tends to overstate, since each child's p95 is above most of its samples. No sketch is stored

## DB corruption

If integrity checks indicate corruption:
- stop normal writes
- enter degraded/read-only mode if possible
- prominently warn the admin
- provide restore instructions
- never automatically overwrite with a backup

## Disk space

System diagnostics should show:
- DB size
- backup size
- filesystem free space if available

Warnings:
- <10% free: warning
- <5% free: critical

Do not auto-delete beyond configured retention just because disk is low.

## Filesystem layout

All persistent data under one directory:

```text
/data/
  sinjal.db
  master.key
  backups/
  uploads/
```

Uploads are limited to status-page logos or similarly explicit assets.

## Backup consistency

Use SQLite backup API or safe checkpoint/copy procedure. Do not copy a live DB file unsafely without accounting for WAL.
