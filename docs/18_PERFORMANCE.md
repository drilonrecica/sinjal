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
