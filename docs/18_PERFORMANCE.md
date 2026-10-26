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
- self-hosted fonts: ≤150 KB total woff2 (separate from the JS budget; cached immutably). Measured (M8-01): Inter 29.9 KB + JetBrains Mono 15.5 KB = 45.4 KB. A test in `internal/assets` fails above 150 KiB.
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

The scenario numbers are used in the "Scenario" column of the tables under [Measured](#measured).

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

Development machine: Intel Core i5-7500 (4 cores), Linux, Go 1.27, `go test -bench`. Run with `make bench`.

Numbers are for comparison between changes on the same machine, not promises. Each row is the measurement recorded at the task named; later rows do not rewrite earlier ones.

Architecture facts behind these numbers:

- Every result is written by the one processor goroutine in batched transactions.
- No worker writes to SQLite, and no goroutine exists per monitor.

Where each scenario is measured:

| Scenario | Where |
|---|---|
| 1 scheduler insert/update/pop | [M2](#m2-m2-20) |
| 2 1,000 due monitors | [M2](#m2-m2-20) |
| 3 result batching | [M2](#m2-m2-20), [M3](#m3-m3-02-to-m3-14), [M8](#m8-m8-05-to-m8-17) |
| 4 raw insert | [M2](#m2-m2-20) |
| 5 rollup aggregation | [M6](#m6-m6-05-and-m6-08) |
| 6 monitor list | [M3](#m3-m3-02-to-m3-14), [M8](#m8-m8-05-to-m8-17) |
| 7 incident open/close | [M3](#m3-m3-02-to-m3-14) |
| 8 SSE fan-out | [M2](#m2-m2-20) |

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

### M3 (M3-02 to M3-14)

Result batching:

| Scenario | Benchmark | Result |
|---|---|---|
| 3 result batching | `results.BenchmarkProcessorBatch`, now with incidents, intents and flapping in the transaction | 89–90 µs per result (86–87 µs at M2), 75 allocations (73). The difference is the `flapping_since` column read with the monitor's state; incident, intent and flapping statements run only when the state changes |
| 3 result batching | same, with parent suppression (M3-05) | 98–100 µs per result (92–97 µs on the same run without it), 79 allocations (75). In this benchmark one monitor in ten fails every check and stays DOWN; each costs one indexed read per batch to know whether its DOWN is held back (`store.PendingDown`). The parent is read only when an intent is decided |
| 3 result batching | `results.BenchmarkProcessorBatch` with the incident-change list (M3-11) | 101–105 µs per result on a noisy run, 79 allocations, unchanged from M3-05: a change is recorded only when an incident opens, updates or closes, and announced after the commit |

Reads and pages:

| Scenario | Benchmark | Result |
|---|---|---|
| uptime | `store.BenchmarkUptime` (one monitor, one year, 2,000 incidents, 50 pauses, a daily excluded window) | 3.2 ms, 12,500 allocations, mostly parsing the 2,000 incident rows and expanding 365 occurrences; one call per monitor and range shown, not for the list of 1,000 |
| uptime, one day (M3-12) | `store.BenchmarkUptimeDay` (one monitor, last 24 hours, one incident, one daily window) | 138 µs and 159 allocations. For 1,000 monitors that is about 140 ms, so the list does not show uptime and the detail header does (one read per header render) |
| history read | `store.BenchmarkLatencyHistory7d` (one monitor, a week at 30 s: 20,160 raw rows among 40,320; summary with exact p95 and a 720-point series) | 21 ms, 121,000 allocations (rows scanned in Go). Three SQL statements (aggregates, `ORDER BY` for p95, `GROUP BY` for buckets) took 85 ms. The cost is linear in raw rows in range: until M6 rollups and retention, a 30-day range on raw data is about 4× this. Long ranges: see M6 |
| sparkline data | last 30 results of each of 1,000 monitors in one query (correlated `LIMIT 30` per monitor) | 48 ms, 150,000 allocations: over the list's 50 ms budget on its own, so the sparkline is on the detail header (one monitor, one index probe) and not in the list. Replaced in M8 by the in-memory ring |
| 7 incident open/close | `store.BenchmarkIncidentOpenClose` (one transaction per open + close, committed on its own, among 1,000 monitors and 10,000 ended incidents) | 184 µs, 101 allocations: about 15 statements and one commit, the worst case; inside a processor batch the commit is shared. An incident happens a few times a day per monitor, so this is never on a hot path (M3-14) |
| 6 monitor list | `store.BenchmarkMonitorList` (the four queries of the list for an admin: monitors, tags, last durations, URLs; 1,000 monitors, 100 raw results each) | 17.7 ms, 56,000 allocations (M3-14); 18–23 ms, same allocations, with the URLs query widened to every type's target (M4-06) |
| 6 monitor list page | `web.BenchmarkMonitorListPage` (`GET /monitors` as an admin through the whole handler, session included; same data) | 24 ms, 96,000 allocations, 8 MB, 953 KiB of HTML per page: inside the 50 ms budget. The page is large uncompressed, which gzip and a 1,000-row list make acceptable; pagination or filtering would be a UX decision, not a performance one (M3-14). 26–30 ms after M4-06 (`store.Targets`, a UNION over the five config tables) |
| page weight | first-party + vendored JS, gzip -9 | 47.0 KB of the 100 KB budget: htmx 17.1, uPlot 22.3, SSE extension 2.8, chart.js 2.0, passkey.js 1.8, live.js 1.0. uPlot and chart.js loaded only on a History tab with data (until M8-05) |

### M4

M4-05, heartbeat beat: `Engine.Beat` (token hash, one indexed `UPDATE … RETURNING`, two indexed reads, a scheduler command, a result to the processor) took about 200 µs each over 2,000 sequential beats on a real database file, with the processor writing them at the same time. Beats are rare (one per monitor per interval) and arrive on HTTP handlers, not on the worker pool. No benchmark is kept for them.

### M6 (M6-05 and M6-08)

| Scenario | Benchmark | Result |
|---|---|---|
| history read, long ranges (M6-05) | `store.BenchmarkLatencyHistory1y` (the week of raw results of `BenchmarkLatencyHistory7d`, then 23 days of 5-minute and 335 days of hourly buckets: 14,664 rolled rows; approximate p95, 720 points) | 55 ms, 240,000 allocations; 90 days (raw week, 23 days of 5m, 60 days of 1h) 41 ms; 7 days unchanged at 21 ms. About 2 µs per rolled row, nearly all of it the driver handing the row over (`count(*)` over the same rows is 0.85 ms for 8,000). Measured and not faster: SQL `GROUP BY` per chart bucket (7.7–12 ms per 8,000 rows) plus a p95 query (11.7 ms), a `WITHOUT ROWID` table (no change). Scanning plain numbers instead of NULL-aware types: 59 → 55 ms, 14 → 6.7 MB. The 1-year range is about 5 ms over the 50 ms page target; it is the rarest range, and the cost is linear in the rows a year leaves after retention. Accepted by the owner (`26`, M6-05) |
| 5 rollup aggregation (M6-08) | `retention.BenchmarkRollup` (one daily run over one monitor with a backlog in two tiers: three days of raw results at 30 s, 8,640 rows into 5-minute buckets, and ten days of 5-minute buckets, 2,880 into hourly buckets; fresh database per iteration, seeding outside the timer) | 45 ms per run for 11,516 source rows rolled and 1,104 buckets written (about 4 µs per source row), 3.2 MB and 107,000 allocations. Each step is its own transaction of at most a day of source (`retention.Slice`), so the writer is never held longer than one slice; the daily run is off every hot path. Steady state is one day of backlog per monitor, so 1,000 monitors cost about 1,000 × 15 ms ≈ 15 s of writer time spread over the run, in slices. There is no numeric budget for scenario 5; recorded for comparison |

### M7

| Task | Benchmark | Result |
|---|---|---|
| M7-04 | `web.BenchmarkStatusPage` (a public page of 25 services, each with an incident every three days over 90 days) | Cold build (25 monitor reads, 25 interval reads over 90 days, 2,250 strip days, the incident list): 19.5 ms, 4.6 MB, 24,000 allocations. A visit within the 10 s cache, through the whole handler: 2.2–2.9 ms. The page is 245 KB of HTML uncompressed (about 100 bytes per strip day, very repetitive); one label per bar, no duplicate hidden text. Inside the 50 ms page budget cold and cached |
| M7-06 | `web.BenchmarkHostRouter` (what every request pays for custom hostnames: one primary-key read of the request's host) | 14 µs, 20 allocations. No cache: mappings change in the admin and must apply at once |
| M7-10 | `BenchmarkStatusPage/api.json`, `BenchmarkStatusPage/feed.xml` (through the whole handler from the cached view) | `api.json` (25 services, 10 KB) 0.26 ms and `feed.xml` (14 KB) 0.39 ms per request; a cold build is the 19 ms above |

The redesigned status page (M8-17) is measured under M8.

### M8 (M8-05 to M8-17)

Beat bars (M8-16):

| Scenario | Benchmark | Result |
|---|---|---|
| ring push | `recent.BenchmarkPush` (1,000 full rings) | 79 ns, no allocation, per stored result on the processor goroutine |
| ring read for the list | `recent.BenchmarkNewestList` (36 checks of each of 1,000 monitors into one buffer) | 0.18 ms, no allocation; the SQL read it replaces cost 48 ms (M3 "sparkline data") |
| ring memory | `recent.TestMemoryAt1000` | 3.9 MiB for 1,000 monitors × 120 checks (32 bytes a check); rings exist only for monitors with checks. The status code added for Live activity (M8-15 follow-up, `int16`) sits in the padding after `OK`: still 32 bytes, pinned by `recent.TestCheckSize` |
| 3 result batching | `results.BenchmarkProcessorBatch`, with the ring push wired as in production | 102–103 µs per result, 82 allocations: unchanged (98–105 µs at M3) |
| start-up seed | `store.BenchmarkRecentChecks` (newest 120 of each of 1,000 monitors with 150) | 300–450 ms (noisy), 1.1 M allocations: nearly all the driver handing 120,000 rows over; an `ORDER BY` added about 90 ms, so each monitor's few checks are sorted in Go. Too slow to hold the start (cold start <1 s), so the engine seeds in the background beside the first checks and `recent.Store.Seed` merges what was pushed meanwhile. Reading `protocol_status` too (M8-15 follow-up) costs about 25 ms and 7 MB more garbage once at start (about 330 against 305 ms, 48 against 41 MB, same allocations); reading it only for failures saved nothing, since the cost is the column itself |
| 6 monitor list page | `web.BenchmarkMonitorListPage` with full beat bars | 28–30 ms (26–30 ms before), 2.2 MB of HTML, 63 KB gzip. With a tooltip on every beat it was 45 ms and 3.8 MB (84 KB gzip), 90 % of the budget, after building tips with one buffer per request (48 ms before that): list tips are fetched on the first hover instead (owner decision, M8-16) |
| row tips on hover | `web.BenchmarkMonitorDetail/tips` | 0.11 ms, from memory |
| page weight | `js/beats.js`, gzip -9 | 2.0 KB, on every live page (`LiveScripts`): 49.0 KB of the 100 KB JS budget in total |

Shell, column and palette (M8-05, M8-06):

| Scenario | Benchmark | Result |
|---|---|---|
| column uptime (M8-05) | `store.BenchmarkUptimesRaw` (24 h of 1,000 monitors over a year of history: 20 incidents each, one in ten active, 50 pauses; the incident read scans the table) | 11–18 ms (noisy machine), 9,400 allocations: one read transaction for all monitors instead of the 140 µs per monitor of `store.Uptime` (M3-12) |
| 6 monitor column (M8-05) | `web.BenchmarkMonitorListPage`, now `GET /fragments/monitors` (the shell's column) with uptime for every row: one incident per monitor today, one in ten active | 21–33 ms over six samples on a machine at load 8 (two unrelated JVMs), 2.0 MB of HTML, 63 KB gzip: inside the 50 ms budget, with uptime the old list could not afford. The column is fetched once per page load; moving between pages swaps only the pane |
| shell assets (M8-05) | every shell page links one fixed set (`ShellStyles`, `ShellScripts`), gzip -9 | 20.0 KB CSS, 52.5 KB JS (htmx 17.1, uPlot 22.3, SSE 2.8, shell.js 2.3, chart.js 2.3, beats.js 2.0, passkey.js 1.9, live.js 1.2, copy.js 0.6): within the 100 KB JS budget; cached immutably, so only the first page pays it. uPlot no longer waits for a History tab: a page swapped into the pane cannot bring scripts of its own |
| command palette (M8-06) | `js/palette.js`, gzip -9; `GET /fragments/palette` | 2.4 KB, in `ShellScripts`: 54.9 KB of the 100 KB JS budget. The search reads the monitors, tags, display labels and (admins) targets in four queries whatever the count, matches in Go and stops at 8; the input waits 150 ms after typing and a newer request replaces an older one (`hx-sync`) |
| detail Overview | `web.BenchmarkMonitorDetail/overview` (120-beat header, 90-day strip with an incident every three days, 24-hour graph) | 1.5–1.7 ms |

Payload (M8-14):

| Scenario | Benchmark | Result |
|---|---|---|
| payload check (M8-14) | every file under `web/static`, `gzip -9`, summed over what `ShellScripts`, `ShellStyles` and the layout link | **JS 58.3 KB of the 100 KB budget** (htmx 17.1, uPlot 22.3, SSE 2.8, palette 2.4, toast 2.4, chart.js 2.3, shell.js 2.3, beats.js 2.0, passkey.js 1.9, live.js 1.2, shortcuts 1.0, copy.js 0.6); the 52.5 KB of M8-05 grew by palette, toast and shortcuts. **CSS 21.9 KB** (tokens 1.3, base 1.9, components 4.3, then the shell's 14 sheets 14.5). **Fonts 45.5 KB** woff2 (Inter 29.9, JetBrains Mono 15.6) against the 150 KB cap; woff2 does not gzip further. Total first load about 125.7 KB, all immutable. The public status page links tokens, base, components and public.css (8.9 KB gzip) and the Inter preload, with no first-party JS. Headroom: 41.7 KB of JS |
| payload check (monitor form redesign, 2026-10-25) | same method | **JS 59.8 KB** of the 100 KB budget: `monitor_form.js` 0.8 KB is new, shell.js grew to 3.0 KB with the sign-off phone menu and the 422 swap. **CSS 24.1 KB** (`monitor_form.css` 2.9 KB). Headroom: 40.2 KB of JS |

Status page redesign (M8-17): the redesigned page was measured against the code before it, back to back on the same loaded machine (load average 12, so the absolute times are not comparable with M7-04's).

- Cold build: 37–51 ms against 44–52 ms.
- Cached visit: 2.8–3.8 ms against 5.5–7.1 ms.
- HTML: 212 KB against 255 KB. The shared day bars carry `data-day` instead of two classes per bar.
- The hero sentence and the note use what the build already reads.

Status page layouts, shared foundation (SL-03): `web.BenchmarkStatusPage`, load average 3.5–5, three runs.

- Cold build: 20–31 ms, 4.5 MB, 25,100 allocations (M7-04: 24,000). The new fields (`Since`, `Downtime`, `LastIssue`, `Calendar`) are computed from the strips and incidents the build already reads: no new query. The calendar walks 90 days × 25 services once, and each listed incident over the 90 days.
- Cached visit: 1.3–1.5 ms; the HTML is 213 KB.
- `api.json` 0.27 ms (10.1 KB) and `feed.xml` 0.39 ms (14.5 KB), unchanged; their bytes do not depend on the layout (`TestPublicMachineFormsIgnoreLayout`).
- CSS, gzip -9: the shared `public.css` 1.8 KB and `public_sentence.css` 1.0 KB, together 2.8 KB against the old single `public.css` of 1.5 KB (the split repeats a little and adds the "Monitoring since" and footer rules).
- Inside the 50 ms page budget, cold and cached.

Sentence layout (SL-04): `web.BenchmarkStatusPage`, load average 3.8–5.3, three runs.

- Cold build: 20.2–20.8 ms, 4.5 MB, 25,380 allocations. The headline, the time so far, the counts and the incidents' date, time and words come from what the build already reads.
- Cached visit: 1.2–1.9 ms; the HTML is 216 KB (213 KB before: each service carries a Terminal tag, each incident a date column).
- `api.json` 0.28–0.37 ms (10.1 KB) and `feed.xml` 0.39–0.43 ms (14.5 KB), unchanged.
- CSS, gzip -9: `public.css` 1.9 KB and `public_sentence.css` 2.0 KB, under the 6 KB per layout sheet.

Signal layout (SL-05): `web.BenchmarkStatusPage` (the page in its default layout, Sentence; the view is the same for every layout and Signal adds one string per incident), load average 5.8–6.4, three runs.

- Cold build: 20.4 ms, 4.5 MB, 25,220 allocations. `Short` is computed from the duration the build already has.
- Cached visit: 1.0–1.2 ms; the HTML is 215 KB.
- `api.json` 0.29–0.40 ms (10.1 KB) and `feed.xml` 0.39–0.44 ms (14.5 KB), unchanged.
- CSS, gzip -9: `public_signal.css` 2.7 KB, under the 6 KB per layout sheet. The lamp is CSS only: no image, no script.

Logbook layout (SL-06): `web.BenchmarkStatusPage` (the page in Sentence; the view is shared), load average 2.6–3.6, three runs.

- Cold build: 19.3–23.2 ms, 4.5 MB, 25,410 allocations. The calendar's placement, row labels, upcoming days, long titles and "others fine" flags come from the rows and incidents the build already has; no new query.
- Cached visit: 1.2–1.6 ms; the HTML is 215 KB. The same 25-service page in Logbook is 54 KB (4.6 KB gzip, against Sentence's 4.3 KB): the calendar's 95 cells with their words replace 2,250 strip bars.
- `api.json` 0.30–0.43 ms (10.1 KB) and `feed.xml` 0.40–0.46 ms (14.5 KB), unchanged.
- CSS, gzip -9: `public_logbook.css` 3.6 KB, under the 6 KB per layout sheet; a third of it is the 56 attribute-to-grid-line rules that place the cells without inline styles.

Heartline layout (SL-07): the new `web.BenchmarkStatusPageHeartline` is `BenchmarkStatusPage`'s 25-service page in Heartline with "Show latency" on for every service and a check every minute over the last day (36,000 raw results, 1,440 per service), so a cold build reads 25 days of results through `store.LatencyHistory` (24 hours in 96 buckets each) beside what every layout reads. Load average 2.3–3.3, three runs.

- Cold build, one service at a time (the build as it was): **78–81 ms**, 9.6 MB, 250,000 allocations. Profiled, 53 % of it is `LatencyHistory`, and nearly all of that SQLite stepping the raw rows (about 1 µs a row: a seek into the table for each index hit, and the crossing into Go); the Go-side scan and parse are minor. A covering index or a query of its own is ruled out by the layout's rule (no new storage, no new query).
- Cold build with the services read in parallel (`Public.rows`, `publicRowWorkers` = 4, the reader pool's least size): **37–39 ms**, same bytes and allocations. Inside the 50 ms budget on this 4-core machine. The same change takes the Sentence page's cold build (`BenchmarkStatusPage`) from 22–26 ms to **9.6–9.8 ms**. On one core the parallel reads gain nothing and the Heartline cold build stays near 80 ms; a page's cold build happens at most once per 10 s and visitors are served from the cache.
- The pulse cost is linear in the raw results of the last day: a page whose services check every 30 s (the default interval) reads twice the rows, so its cold build is about 65 ms on four cores. Trimming `LatencyHistory`'s per-row work (scanning `unixepoch(checked_at)` instead of parsing the RFC 3339 string) would help every caller and is left for the owner to weigh.
- Cached visit: **0.95–1.05 ms**; the HTML is 260 KB (Sentence 215 KB): 25 pulses of about 1.8 KB of path data each.
- `api.json` and `feed.xml` unchanged (they are measured on the Sentence page; their bytes do not depend on the layout).
- CSS, gzip -9: `public_heartline.css` 3.2 KB, under the 6 KB per layout sheet. The pulse is server-side SVG: no script, no image.

Status page layouts, stylesheets (SL-08): every public sheet, `gzip -9`, after the layout tests' fixes (each layout's sheet now styles the state glyph; Signal's lamp highlight changed its mix only).
- Shared `public.css` 1.9 KB (5.6 KB raw), linked on every public page with tokens, base and components.
- `public_sentence.css` 2.0 KB (7.5 KB raw), `public_signal.css` 2.7 KB (10.1 KB raw), `public_heartline.css` 3.2 KB (12.2 KB raw), `public_logbook.css` 3.6 KB (14.0 KB raw): all under the 6 KB of a layout sheet. A page links the shared sheet and its layout's alone, so the heaviest, Logbook, adds 5.5 KB of public CSS.
- `web.TestPublicLayoutSheetsSize` fails a layout sheet at 6 KiB gzip -9, so the budget is kept by the tests, not only recorded here.
