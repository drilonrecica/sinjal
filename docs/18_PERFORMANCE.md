# Performance Budgets and Benchmarks

## Budgets

### Runtime
- idle RAM: <50 MB target
- typical idle RAM aspiration: <35 MB
- idle CPU: effectively ~0%
- cold start: <1 second target on ordinary small VPS hardware

### Web
- normal localhost dashboard HTML response: <50 ms target
- total compressed first-party JS: <100 KB
- self-hosted fonts: ≤150 KB total woff2 (separate from the JS budget; cached immutably)
- avoid large client-side hydration/runtime

### Container
- target image size: <50 MB

### Monitoring scale
- 100 monitors: trivial
- 1,000 monitors: tested and usable
- 10,000 monitors: not promised

## Benchmark scenarios

Create reproducible benchmarks for:

1. scheduler insert/update/pop
2. 1,000 due monitors with bounded worker pool
3. result processor batching
4. raw result insert throughput
5. rollup aggregation
6. dashboard monitor list query
7. incident open/close transaction
8. SSE fanout to realistic small client count

## Load scenario

Synthetic local targets should cover:
- fast 200 responses
- 5s timeout
- DNS failure
- connection refused
- 1MiB body
- redirect chain within configured limit
- 1,000 monitor schedule distribution

## Regression discipline

Do not micro-optimize every code path.

Do investigate if:
- idle RAM moves materially upward
- JS payload exceeds budget
- DB writes become one transaction per worker
- goroutine count grows with monitor count at idle
- container image bloats
- startup performs unnecessary network calls

## Measured

Development machine: Intel Core i5-7500 (4 cores), Linux, Go 1.27, `go test -bench`. Numbers are for comparison between changes on the same machine, not promises. Run with `make bench`.

### M2 (M2-20)

| Scenario | Benchmark | Result |
|---|---|---|
| 1 scheduler insert | `scheduler.BenchmarkQueueInsert` (add + remove on a full heap) | 482 ns at 1,000 monitors, 608 ns at 10,000; 1 allocation |
| 1 scheduler update | `scheduler.BenchmarkQueueUpdate` (edit: new interval, immediate run) | 57 ns at 1,000, 66 ns at 10,000; no allocation |
| 1 scheduler pop | `scheduler.BenchmarkQueuePop` (steady state, one due monitor per pop) | 304 ns at 1,000, 427 ns at 10,000; no allocation |
| 2 1,000 due monitors | `scheduler.BenchmarkDueMonitors` (all due at once, default workers, 10 ms per check) | 650 ms per burst with 16 workers (the ideal is 625 ms); nothing rejected, no late start, 17 goroutines in total for the pool (workers, not monitors) |
| 3 result batching | `results.BenchmarkProcessorBatch` (1,000 monitors, real SQLite file, production batch size and flush time) | 86 µs per result, about 9,000–11,000 results/s including state machine and monitor row update. 1,000 monitors at the default 30 s produce about 33 results/s |
| 4 raw insert | `store.BenchmarkInsertCheckResult` (128 rows per transaction) | 16.6 µs per row (about 60,000 rows/s), 14 allocations |
| 8 SSE fan-out | `sse.BenchmarkPublish` (one event, 8 clients) | 0.9 µs, 4 allocations (M2-14) |

### M3 (M3-02 to M3-10)

| Scenario | Benchmark | Result |
|---|---|---|
| 3 result batching | `results.BenchmarkProcessorBatch`, now with incidents, intents and flapping in the transaction | 89–90 µs per result (86–87 µs at M2), 75 allocations (73). The difference is the `flapping_since` column read with the monitor's state; incident, intent and flapping statements run only when the state changes |
| 3 result batching | `results.BenchmarkProcessorBatch` with the incident-change list (M3-11) | 101–105 µs per result on a noisy run, 79 allocations, unchanged from M3-05: a change is recorded only when an incident opens, updates or closes, and announced after the commit |
| 3 result batching | same, with parent suppression (M3-05) | 98–100 µs per result (92–97 µs on the same run without it), 79 allocations (75). In this benchmark one monitor in ten fails every check and stays DOWN; each costs one indexed read per batch to know whether its DOWN is held back (`store.PendingDown`). The parent is read only when an intent is decided |
| uptime | `store.BenchmarkUptime` (one monitor, one year, 2,000 incidents, 50 pauses, a daily excluded window) | 3.2 ms, 12,500 allocations, mostly parsing the 2,000 incident rows and expanding 365 occurrences; one call per monitor and range shown, not for the list of 1,000 |
| history read | `store.BenchmarkLatencyHistory7d` (one monitor, a week at 30 s: 20,160 raw rows among 40,320; summary with exact p95 and a 720-point series) | 21 ms, 121,000 allocations (rows scanned in Go). Three SQL statements (aggregates, `ORDER BY` for p95, `GROUP BY` for buckets) took 85 ms. The cost is linear in raw rows in range: until M6 rollups and retention, a 30-day range on raw data is about 4× this |
| history read, long ranges (M6-05) | `store.BenchmarkLatencyHistory1y` (the week of raw results above, then 23 days of 5-minute and 335 days of hourly buckets: 14,664 rolled rows; approximate p95, 720 points) | 55 ms, 240,000 allocations; 90 days (raw week, 23 days of 5m, 60 days of 1h) 41 ms; 7 days unchanged at 21 ms. About 2 µs per rolled row, nearly all of it the driver handing the row over (`count(*)` over the same rows is 0.85 ms for 8,000). Measured and not faster: SQL `GROUP BY` per chart bucket (7.7–12 ms per 8,000 rows) plus a p95 query (11.7 ms), a `WITHOUT ROWID` table (no change). Scanning plain numbers instead of NULL-aware types: 59 → 55 ms, 14 → 6.7 MB. The 1-year range is about 5 ms over the 50 ms page target; it is the rarest range, and the cost is linear in the rows a year leaves after retention. Accepted by the owner (`26`, M6-05) |
| sparkline data | last 30 results of each of 1,000 monitors in one query (correlated `LIMIT 30` per monitor) | 48 ms, 150,000 allocations: over the list's 50 ms budget on its own, so the sparkline is on the detail header (one monitor, one index probe) and not in the list |
| 7 incident open/close | `store.BenchmarkIncidentOpenClose` (one transaction per open + close, committed on its own, among 1,000 monitors and 10,000 ended incidents) | 184 µs, 101 allocations: about 15 statements and one commit, the worst case; inside a processor batch the commit is shared. An incident happens a few times a day per monitor, so this is never on a hot path (M3-14) |
| 6 monitor list | `store.BenchmarkMonitorList` (the four queries of the list for an admin: monitors, tags, last durations, URLs; 1,000 monitors, 100 raw results each) | 17.7 ms, 56,000 allocations (M3-14); 18–23 ms, same allocations, with the URLs query widened to every type's target (M4-06) |
| 6 monitor list page | `web.BenchmarkMonitorListPage` (`GET /monitors` as an admin through the whole handler, session included; same data) | 24 ms, 96,000 allocations, 8 MB, 953 KiB of HTML per page: inside the 50 ms budget. The page is large uncompressed, which gzip and a 1,000-row list make acceptable; pagination or filtering would be a UX decision, not a performance one (M3-14); 26–30 ms after M4-06 (`store.Targets`, a UNION over the five config tables) |
| page weight | first-party + vendored JS, gzip -9 | 47.0 KB of the 100 KB budget: htmx 17.1, uPlot 22.3, SSE extension 2.8, chart.js 2.0, passkey.js 1.8, live.js 1.0. uPlot and chart.js load only on a History tab with data |

Every result is written by the one processor goroutine in batched transactions; no worker writes to SQLite and no goroutine exists per monitor. Scenario 5 (rollups) is measured with its feature (M6): see M6-08 below.

M3-12: `store.BenchmarkUptimeDay` (one monitor, last 24 hours, one incident, one daily window): 138 µs and 159 allocations. For 1,000 monitors that is about 140 ms, so the list does not show uptime and the detail header does (one read per header render).

### M4

M4-05: a heartbeat beat (`Engine.Beat`: token hash, one indexed `UPDATE … RETURNING`, two indexed reads, a scheduler command, a result to the processor) took about 200 µs each over 2,000 sequential beats on a real database file, the processor writing them at the same time. Beats are rare (one per monitor per interval) and arrive on HTTP handlers, not on the worker pool; no benchmark is kept for them.

### M6 (M6-08)

| Scenario | Benchmark | Result |
|---|---|---|
| 5 rollup aggregation | `retention.BenchmarkRollup` (one daily run over one monitor with a backlog in two tiers: three days of raw results at 30 s, 8,640 rows into 5-minute buckets, and ten days of 5-minute buckets, 2,880 into hourly buckets; fresh database per iteration, seeding outside the timer) | 45 ms per run for 11,516 source rows rolled and 1,104 buckets written (about 4 µs per source row), 3.2 MB and 107,000 allocations. Each step is its own transaction of at most a day of source (`retention.Slice`), so the writer is never held longer than one slice; the daily run is off every hot path. Steady state is one day of backlog per monitor, so 1,000 monitors cost about 1,000 × 15 ms ≈ 15 s of writer time spread over the run, in slices. There is no numeric budget for scenario 5; recorded for comparison |
