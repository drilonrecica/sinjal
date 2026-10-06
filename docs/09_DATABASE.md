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

## Busy handling

Transient `SQLITE_BUSY`:
- bounded retry/backoff
- example: 25ms, 100ms, 250ms, 1s
- after limit, surface a system warning/error

Do not silently drop results.

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
