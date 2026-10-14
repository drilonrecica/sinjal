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

Built (`internal/db`):
- the baseline pragmas above, applied to every connection through the DSN
- one writer handle with a single connection (`MaxOpenConns=1`): the only write path
- a reader pool of `max(4, NumCPU)` connections opened with `query_only=1`, so it cannot write

## Write architecture

Workers do not independently hammer SQLite.

Use:
- a buffered result channel
- one result-processing path
- batched transactions for check-result inserts where useful

The result processor is responsible for:
- persisting the check result
- applying the state transition
- opening/closing incidents
- enqueueing notification intents
- emitting SSE updates

Keep transaction boundaries clear.

Built (`internal/results`, SQL in `internal/store/results.go` and `internal/store/incidents.go`):

Batching:
- workers call `Processor.Add`; a buffered channel of 256 results feeds one goroutine
- a batch is written when it holds 128 results or 200 ms after its first result, whichever comes first. Nothing wakes up while no result is waiting.
- a result for a monitor deleted or paused in the meantime (a check that was already running) is discarded and counted, not stored

One transaction per batch. For each result, in arrival order:
1. read the monitor's state and thresholds
2. insert the `check_results` row
3. apply the state machine (`10_INCIDENTS.md` "State machine")
4. update the monitor row: `current_state`, `current_state_since` (when the state changed), `last_check_at`, `last_success_at` or `last_failure_at`, `tls_not_after`

In the same transaction, as needed:
- when the state becomes DOWN, the incident and its `detected` and `declared_down` events are inserted; when it leaves DOWN, the incident is closed with a `recovered` event (`10_INCIDENTS.md` "Opening incident", "Recovery"). Results that do not change the state cost no extra statement.
- `tls_not_after` is replaced when the check saw a certificate, cleared when a check succeeded without one, and kept on a failed check that saw none
- opening or closing an incident also decides the notification intent (`down`, `recovery`) and whether it is suppressed. A suppressed one is recorded as an incident event (`10_INCIDENTS.md` "Notification intents").
- an incident opening or closing is also a flapping transition: the monitor's last incidents are read, and `flapping_since` is set when it is the fourth within 10 minutes. A flapping monitor's overlay is cleared by the first result 10 minutes after its last transition (`10_INCIDENTS.md` "Flapping").
- deciding an intent reads the parent's state when the monitor has a parent (one indexed read). A DOWN monitor costs one read per batch to know whether its DOWN is held back (`10_INCIDENTS.md` "Parent dependency").

Only after the commit:
- the batch's intents are logged and handed to the dispatcher
- incidents opened, updated or closed are announced (`incident.*`, `32_SSE_EVENTS.md`)
- the confirmation retry is requested from the scheduler
- the monitor is announced as `monitor.updated`, once per monitor per batch

## Busy handling

Transient `SQLITE_BUSY`:
- bounded retry/backoff
- example: 25ms, 100ms, 250ms, 1s
- after the limit, surface a system warning/error

Do not silently drop results.

Built: `db.Retry` retries only `SQLITE_BUSY`, waiting 25 ms, 100 ms, 250 ms, then 1 s. After the last retry it returns a `*db.BusyExhaustedError`, which callers must surface. The function passed to `Retry` is re-run in full, so it must contain a complete transaction.

When the result processor cannot write a batch (busy through every retry, or any other error):
- the batch is kept in memory and tried again after 1 s, until it is stored
- an ERROR is logged (at most once a minute while it lasts) and `Processor.Stats().Warning` is set; the next successful write clears it. Home shows the warning in its problem strip.
- once the held batch is full, the processor stops taking results: the channel fills, workers wait, the worker queue fills and the scheduler skips checks (`07_SCHEDULER.md` "Worker pool"). Memory stays bounded, and no stored history is invented or lost silently.
- at shutdown the queued results are written once more within 5 s. If that fails, the number of lost results is logged.

## Retention jobs

Daily internal job:
1. roll raw records older than 7d into 5m buckets
2. roll 5m buckets older than 30d into 1h buckets
3. roll 1h buckets older than 365d into 1d buckets
4. delete source rows after a successful rollup transaction
5. conditionally optimize/vacuum only if justified (`PRAGMA optimize` after every successful rollup; never VACUUM, see "Maintenance after the rollup")

Do not VACUUM every day by default.

### Daily runner

Built (`internal/jobs`, started by `serve`):
- runs once a day at **04:00 in the instance time zone** (`SINJAL_TIMEZONE`). That hour is quiet for a personal instance and outside the hours DST skips or repeats in Europe and North America, so the job runs exactly once per local day.
- at startup it runs at once when overdue: no recorded run, or the last one before the latest 04:00. A restart therefore never skips a day and never runs twice in one.
- waits are at most an hour and the clock is read again after each, so a suspended machine or a clock change delays a run by at most that
- a run: the rollup, then `PRAGMA optimize`, then expired-session cleanup
- on success the time is stored in `system_settings` as `last_retention_run` (RFC 3339 UTC); Settings → System shows it (`21_SYSTEM_DIAGNOSTICS.md`, M9)
- a failed rollup is logged as an ERROR and not recorded, so the next start (or the next day) runs again. A failed session cleanup is only logged.
- shutdown cancels a run between rollup steps; `serve` waits for it before closing the database
- backups and the integrity check join this runner in M9

### Rollup

Built (`internal/retention`, SQL in `internal/store/rollup.go`):
- tiers in order: raw → 5m before `now − 7d`, 5m → 1h before `now − 30d`, 1h → 1d before `now − 365d`. Daily buckets are kept.
- each cutoff is floored to the target resolution, so only whole buckets are rolled and a bucket is always built from all of its sources at once
- the run goes per monitor (paused ones too), one **step** at a time. A step is one write transaction that:
  1. finds the monitor's oldest source row (one index probe)
  2. takes the slice from the bucket holding it to one day later, or to the cutoff
  3. builds its buckets (`history.FromRaw` / `history.Merge`)
  4. upserts them (`ON CONFLICT … DO UPDATE`, so a step that runs again for a bucket replaces it)
  5. deletes the slice's sources and commits
- buckets and the deletion of their sources commit together: a failure leaves the source rows intact and writes no bucket
- steps stay small (a day of raw results is 2,880 rows at 30 s) because the writer connection is shared with the result processor, which waits while a step holds it. Starting each step at the oldest remaining row skips empty days, so a paused month costs nothing.
- every step is wrapped in `db.Retry`, and the context is checked between steps, so shutdown stops a run cleanly
- there is no cursor: a run that stopped (error, shutdown) is resumed by the next one from the oldest source row left
- a step that would delete nothing is an error, so the loop cannot spin
- the run stops at its first error and returns it with what it did so far (steps, buckets, rows deleted)

### Maintenance after the rollup

`PRAGMA optimize` runs on the writer once the rollup succeeded:
- it refreshes the planner's statistics (`sqlite_stat1`) for the tables the run used
- SQLite bounds its work with a temporary `analysis_limit`, so it costs little even on a large file
- a failure is logged and changes nothing else; after a failed rollup it is not run

**No VACUUM**, daily or otherwise:
- deleted rows leave free pages on SQLite's freelist, and the next inserts reuse them
- once retention is in steady state (each day rolls about as many rows as a day adds; daily buckets grow by one row per monitor per day), the file stops growing. Its size is bounded by retention, not by age (proven over a simulated year in `internal/retention`, M6-07).
- VACUUM would rewrite the whole file, needs up to twice its size in free disk, and holds the only writer connection, and with it every check result, for the whole time
- it is not needed to shrink backups either: they are written with `VACUUM INTO`, which is compact by construction
- `auto_vacuum` is not enabled, for the same reason: it moves pages on every commit to return space the next day would take back

## History queries

Built.

Ranges (`history.Parse`):
- presets: 1 hour, 24 hours (default), 7, 30 and 90 days and 1 year, ending now. Day presets are calendar days in the instance time zone.
- a custom range is `from`/`to` as local date and time. An end in the future becomes now.
- a custom range must end after it starts, start in the past and span at most 400 days; otherwise the default preset is shown with the reason

`store.LatencyHistory(monitor, from, to, buckets)` reads, inside one read transaction:
- the range's raw results once, along `idx_check_results_monitor_time` (in index order, no sort)
- its rolled-up buckets of every tier once, along the `check_aggregates` key

It returns:
- the summary: checks, failures, samples, current, min, average, max and p95
  - latency figures are over successful checks with a duration; a failure's duration is how long it took to fail and is not latency
  - current is the newest sample in the range; with no raw sample, the newest bucket's average
  - p95 is exact (nearest rank, the sample at rank ⌈0.95 n⌉) when the range holds raw results only
  - when the range holds buckets with samples, p95 is `history.ApproxP95` over the raw samples (weight 1 each) and the buckets' p95 (weighted by their samples), and `Approximate` is set. The History tab then reads "p95 (approximate)" in its figures and its text summary.
- the series: the range in at most `buckets` equal buckets of whole seconds, only those holding results, each with checks, failures, average and max latency. The chart asks for 720. A rolled bucket counts in the chart bucket where it starts, and in the range only when it starts inside it.

Resolution follows from retention:
- rolling deletes the sources of every bucket it writes, so each moment of a monitor's history is in exactly one tier: raw for the last 7 days, 5-minute buckets to 30 days, hourly to 365, daily beyond
- reading every tier over the range picks whichever resolution holds it, with nothing counted twice
- the shared read transaction (one WAL snapshot) keeps a rollup step that commits between the two reads from counting its rows in both
- uptime needs none of this: it reads intervals only (`10_INCIDENTS.md` "Uptime")

Cost:
- one pass in Go is faster than SQL aggregates plus an `ORDER BY` for the percentile plus a `GROUP BY` for the buckets: 21 ms instead of 85 ms for a week of 30 s results (`18_PERFORMANCE.md`)
- about 1 µs per raw row and 2 µs per rolled bucket, nearly all of it the driver handing rows over; a year is about 55 ms (`BenchmarkLatencyHistory1y`)
- rolled buckets are scanned as plain numbers (`unixepoch()`, `coalesce()`): NULL-aware scanning cost twice the allocations

## p95

For raw ranges, calculate exact p95 from raw samples.

For long aggregated ranges, p95 may be approximate unless the implementation stores a compact percentile sketch. Do not add a heavy sketch dependency unless benchmarks justify it.

The UI should not imply SLA-grade percentile precision for rolled-up historical data.

Built bucket math (`internal/history/bucket.go`):
- buckets are aligned to the Unix epoch, so in UTC. A daily bucket is a UTC day, and every bucket of a resolution has the same length across DST changes.
- a 5-minute bucket is built from raw results (`FromRaw`): total, success and failure counts, and min, max, average and **exact** p95 (nearest rank, the rule of raw history) over the successful checks. A bucket of failures only has no latency (NULL columns).
- an hourly or daily bucket is built from the finer buckets it covers (`Merge`): counts summed, the smallest minimum, the largest maximum, the average weighted by successful checks. Every stored result has a duration, so `success_count` is the bucket's sample count. Counts, extremes and average are therefore exact at every tier.
- its p95 is **approximate** (`ApproxP95`): the weighted nearest rank over the children's p95 values, each weighted by its successful checks, i.e. the smallest child p95 at which the cumulative weight reaches ⌈0.95 × total⌉
  - with all weights 1 it is the exact nearest rank, so raw samples and rolled buckets can be combined by the same function
  - over buckets it is an estimate that tends to overstate, since each child's p95 is above most of its samples
  - no sketch is stored

## DB corruption

If integrity checks indicate corruption:
- stop normal writes
- enter degraded/read-only mode if possible
- prominently warn the admin
- provide restore instructions
- never automatically overwrite with a backup

Not built yet (integrity check: M9).

## Disk space

System diagnostics should show:
- DB size
- backup size
- filesystem free space if available

Warnings:
- <10% free: warning
- <5% free: critical

Do not auto-delete beyond configured retention just because disk is low.

Not built yet (M9).

## Filesystem layout

All persistent data lives under one directory:

```text
/data/
  sinjal.db
  master.key
  backups/
  uploads/
```

Uploads are limited to status-page logos or similarly explicit assets.

Built: `datadir.Init` creates the directory and `backups/` and `uploads/` (mode 0700) and checks that it is writable.

Logos (M7-03):
- stored as `uploads/<32 hex>.<png|jpg>`: 128 random bits, mode 0600, never the uploaded name
- `status_pages.logo_path` holds the file name
- a replaced or removed logo, and the logo of a deleted page, is deleted from disk after the database change. A failed deletion leaves an orphan, which is logged.
- backups include `uploads/` (M9)

## Backup consistency

Use the SQLite backup API or a safe checkpoint/copy procedure. Do not copy a live DB file unsafely without accounting for WAL.

Built so far: the mandatory pre-migration backup (`internal/db/migrate.go`) is written with `VACUUM INTO` to `backups/pre-migration-<version>-<UTC time>.db`, mode 0600. If it fails, nothing is migrated. Regular backups follow in M9.
