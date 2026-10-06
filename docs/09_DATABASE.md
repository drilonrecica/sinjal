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
- only after the commit: the confirmation retry is requested from the scheduler and the monitor is announced for SSE (`monitor.updated`, once per monitor per batch; `32_SSE_EVENTS.md`)
- a result for a monitor that has been deleted or paused in the meantime (a check that was already running) is discarded and counted, not stored

Notification intents join this transaction with M3-03.

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

## p95

For raw ranges, calculate exact p95 from raw samples.

For long aggregated ranges, p95 may be approximate unless the implementation stores a compact percentile sketch. Do not add a heavy sketch dependency unless benchmarks justify it.

The UI should not imply SLA-grade percentile precision for rolled-up historical data.

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
