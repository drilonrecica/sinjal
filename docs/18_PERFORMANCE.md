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
| sparkline data | last 30 results of each of 1,000 monitors in one query (correlated `LIMIT 30` per monitor) | 48 ms, 150,000 allocations: over the list's 50 ms budget on its own, so the sparkline is on the detail header (one monitor, one index probe) and not in the list |
| page weight | first-party + vendored JS, gzip -9 | 47.0 KB of the 100 KB budget: htmx 17.1, uPlot 22.3, SSE extension 2.8, chart.js 2.0, passkey.js 1.8, live.js 1.0. uPlot and chart.js load only on a History tab with data |

Every result is written by the one processor goroutine in batched transactions; no worker writes to SQLite and no goroutine exists per monitor. Scenarios 5–7 (rollups, dashboard query, incident transaction) are measured with their features (M3, M6).

M3-12: `store.BenchmarkUptimeDay` (one monitor, last 24 hours, one incident, one daily window): 138 µs and 159 allocations. For 1,000 monitors that is about 140 ms, so the list does not show uptime and the detail header does (one read per header render).
