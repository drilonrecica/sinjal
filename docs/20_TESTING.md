# Testing Strategy

## Philosophy

Correctness of monitoring semantics matters more than broad UI click coverage.

Use a balanced strategy.

## Unit tests

Required for:
- state machine
- status-code expression parser
- text/JSON assertions
- maintenance window evaluation
- quiet hours
- dependency suppression
- flapping detection
- uptime calculations
- aggregation bucket logic
- config validation
- secret envelope encode/decode

## Integration tests

Use:
- real temporary SQLite DB
- local HTTP test servers
- local TCP listeners
- controlled DNS test infrastructure where practical
- fake notification endpoints

Required scenarios:
1. failure -> retry -> DOWN
2. retry success -> no incident
3. DOWN -> success -> recovery
4. flapping detection/suppression
5. maintenance suppression
6. adjusted uptime excludes configured maintenance
7. parent DOWN suppresses child notification
8. restart while incident active
9. notification retry/failure
10. backup then restore
11. migration with pre-backup
12. retention rollup
13. raw deletion only after successful aggregate write
14. full-body cap
15. HTTP redirect behavior
16. timeout cancellation
17. public status page redaction
18. viewer cannot mutate configuration
19. proxy trust rules
20. heartbeat expiry

## UI/browser tests

Small focused set only:
- login
- create HTTP monitor
- status transition renders through SSE/HTMX
- theme switch
- status page access modes
- backup/restore confirmation path if browser-exposed

Avoid a huge fragile browser suite.

Tooling (decision P0-14):
- Go tests using `chromedp` in `tests/browser/`, which has **its own `go.mod`**; chromedp is never a dependency of the product module
- run with `make test-browser`; not part of `make test` or CI
- skipped with a clear message when no Chrome/Chromium is installed
- each test builds and boots the real `sinjal` binary on a temporary data directory and drives it over HTTP
- no screenshots-as-assertions; assert on DOM text, attributes and status labels

## Benchmarks

See `docs/18_PERFORMANCE.md`.

## Race testing

Run Go race detector in development/test environments where feasible.

Pay special attention to:
- scheduler updates
- SSE subscriber handling
- result processor
- notification queue
- shutdown

## Test clocks

Time-dependent logic should be testable using an injectable clock abstraction only where concretely needed.

Do not create a generic application-wide abstraction layer solely for architecture purity.

## Fixtures

Fixtures must never contain real credentials or production URLs.
