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

## Black-box integration tests

`tests/integration` builds the real `sinjal` binary and runs it with a scrubbed environment and a temporary data directory. They run as part of `make test` and `make test-race` (skipped with `go test -short`).

Milestone 0 (`m0_test.go`): boot from an empty data directory; database file (0600), `backups/`, `uploads/` created; migration 001 recorded; `/healthz` and `/readyz` answer 200; `/` renders the app shell; assets are hashed, immutable and gzip-capable; SIGTERM exits 0; a restart on the same directory neither migrates nor creates a backup.

Milestone 1 (`m1_test.go`): startup refuses to run, and creates no key, when `master.key` is missing but encrypted data exists; proxy trust (scenario 19): `X-Forwarded-For` changes the logged `client_ip` only when the peer is in `SINJAL_TRUSTED_PROXIES`; initial setup: the setup link is logged once per start and rotates on restart, the old token is refused, the form creates the admin, then `/setup` is 404 and a restart logs no link; sessions: an expired session is deleted at startup, `POST /logout` deletes the live session and clears the cookie, and the token never reaches the logs.

Auth (`auth_test.go`, M1-19), each on its own server: login rate limit (10 failures, then 429 even with the right password, blocked attempt not audited); CSRF rejection on a signed-in POST (no token, wrong token, another session's token, cross-site `Sec-Fetch-Site`, foreign `Origin`) with the session surviving; viewer mutation 403 on every admin action with a valid CSRF token (scenario 18); an expired session no longer authenticates and its cookie is cleared; a password change ends other sessions and rotates the current one; re-authentication enforced after the window, then unlocks after a correct password; passkey registration and password-less sign-in through `internal/auth/passkeytest`, finish not replayable; TOTP codes single-use, including the enrolment code. Each test asserts that the passwords, tokens and secrets it handled never reach the server log.

Milestone 2 (`m2_test.go`), monitors seeded into the data directory while the server is stopped, against a local target: a stored monitor is checked as soon as the server starts (failure, confirmation retry, DOWN); after a restart with the target still failing a fresh check runs promptly and the monitor is DOWN since the original moment; after a restart with the target healthy it is UP; every SIGTERM exits 0. The same test follows the incident (scenario 8): one active incident from the first failed check, the same single incident after the restart, closed by the recovery with `detected`, `declared_down`, `recovered` as its events. Event stream: `GET /events` redirects to the login when signed out; a signed-in stream receives `monitor.updated` with the monitor's id when a check has been stored; SIGTERM with the stream open ends it cleanly and exits without the shutdown grace period running out (bound 9 s: `net/http` waits up to 5 s for a connection that never sent a request, which Go's client sometimes leaves behind).

M2 scenarios (`m2_scenarios_test.go`, M2-19), one server run against one local target with a path per scenario, verdicts read from `check_results`: 1 failure → confirmation retry → DOWN (two failed results, status and escaped body kept); 2 failure → successful retry → UP, never DOWN (the state is polled throughout); 3 DOWN → recovery → UP since the recovering check; 14 a 16 MiB body whose match lies past the 1 MiB cap fails `body_assertion` and the target cannot finish writing (the check stopped reading); 15 a redirect chain within the limit succeeds, an unfollowed 302 fails `http_status` with status 302, a loop fails `protocol` at 10 redirects; 16 a hanging target fails `timeout` near the 1 s timeout and sees its request cancelled; assertion failures (status, contains, does not contain, JSON value, invalid JSON) each with their own kind and a snippet; passing JSON assertions; no successful check stores a body. A monitor created through the real form (`POST /monitors`, bearer secret) is announced (`monitor.created`), checked at once with its secret, announced again, audited, shown on its page, and after a confirmed delete is gone with its results and secrets, announced as `monitor.deleted`; the token never reaches the log. Restart behaviour is `TestRestartKeepsMonitorState` above.

M3-02 to M3-04 at processor level (`internal/results`, real SQLite, the real processor, scripted results with fixed times): incident lifecycle (`incidents_test.go`), notification intents (`intents_test.go`) and flapping (`flapping_test.go`: entry at the fourth transition, the window edges, suppression while flapping, both exits, an exit that coincides with a transition, restarts mid-window and while flapping, pauses) and parent suppression (`parent_test.go`: the child's DOWN held back and its incident marked, a pending parent holding nothing, parent and child in one batch, the held DOWN decided once after the parent recovers, also across restarts and within one batch, and suppressed by flapping instead when the child flaps) and maintenance (`maintenance_test.go`: the DOWN held back and decided after the window, also when the window ends within the batch, recovery inside a window, `maintenance_overlap` at opening and at close, windows that hold nothing, a daily window in the instance time zone). Window evaluation itself is table-tested in `internal/maintenance` (one-time, daily, weekly mask, across midnight, merged occurrences, DST in Europe/Belgrade and America/New_York: kept local time, skipped and repeated hours, windows over the change, a whole year of a daily window). Uptime (M3-08): the formula table-tested in `internal/incident` (clipping to creation and now, active incidents, overlaps, paused time, maintenance inside and outside observed time, no data, truncation, whole seconds) and against real SQLite in `internal/store` (raw ≠ adjusted with an excluded window, a parent-suppressed incident counted, windows of other monitors ignored, a monitor created paused, the index plans). History (M3-09): range parsing (`internal/history`: every preset, calendar days across DST, custom ranges and each rejection) and `store.LatencyHistory` on real SQLite (summary against a hand computation, failures excluded from latency, exclusive end, empty and failures-only ranges, p95 nearest rank for n = 1, 2, 19, 20, 21, 100, 101, bucket bounds and empty buckets, the index plan without a sort). Charts (M3-10): the timeline's segments (precedence paused > down > maintenance > up, no data before creation, nothing after now, widths and titles), the chart JSON (gaps as null, overlays clipped, active outage to now, marks only for starts in range), the sparkline geometry and label, the summary sentence, the History tab end to end (figures, data attributes, scripts only with data, an invalid custom range explained, the header sparkline) and the CSP test with a populated tab. In Chromium: hover values, the four themes redrawing, 390 px. Scenarios 4 to 7 against the binary are M3-13.

M3-13 (`tests/integration/m3_scenarios_test.go`): scenario 4 against the binary: a target that fails twice and succeeds, repeated, with a one-second interval; the fourth transition sets `flapping_since`, the log shows down, recovery, down, one `flapping` notice and the recovery suppressed by it, the incident events hold the suppression, and the exit is clean. Scenarios 5–7 stay at processor level (`internal/results`).

M4-07 (`tests/integration/m4_scenarios_test.go`), against the binary with one-second intervals set in the database: TCP to an open local port UP and to a closed one DOWN with `connect`; DNS through a local UDP responder UP when the answer holds the expected value and DOWN with `dns_mismatch` (missing value and answer in the snippet) when not; ICMP to 127.0.0.1 UP where pinging is permitted, otherwise only the permission failure. `TestICMPPermissionSurfaced` forces the second case: the binary runs under `unshare -Un` (an unprivileged user and network namespace: no `CAP_NET_RAW`, empty `ping_group_range`), every result is `permission` with the hint naming both fixes, and the monitor is DOWN with an open incident for that reason; it skips where unprivileged user namespaces are disabled. Scenario 20 (`TestHeartbeatExpiryScenario`): a heartbeat monitor created through the form answers with its push URL once (`no-store`); its period is cut to 2 s + 1 s grace in the database; beats by path and by bearer token keep it UP; silence makes it DOWN no earlier than the period, with `heartbeat_missed` ("expected every 2s, grace 1s") and an open incident; a beat closes it; the detail page shows the heartbeat facts and not the token; regenerating issues a new token, the old one answers 404 and the new one 204; neither token reaches the log.

M3-12 (`internal/web/charts_test.go`): every preset as a link and only the shown one current, the custom range kept in the form and without a current preset, the Overview tab with the 24-hour chart and no selector whatever the query says, the header uptime raw and adjusted (seeded outage inside an excluded window), the list without it; the selector in the CSP test.

M3-11: incident changes at processor level (`internal/results/changes_test.go`: opened once, closed on recovery, updates for held and released DOWNs, nothing for a blip), the pause close announced by `Engine.Pause` (`TestPauseAnnouncesTheClosedIncident`), the hub frame with two ids, the store readers and their query plans (`internal/store/incident_read_test.go`), and the pages end to end (`internal/web/incidents_test.go`: list order for admin and viewer, fragment per monitor, the Incidents tab, timeline, notes: refusals, escaping, audit row, `incident.updated` on a real stream, viewer 403).

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

## Manual notification verification

Automated tests use fake notification endpoints. Real delivery to Telegram, Discord and SMTP is verified manually (M5 gate) with the owner's own test accounts.

Credentials live only in a local, git-ignored `.env` file and are never committed, logged or used in CI:

```text
SINJAL_TEST_TELEGRAM_BOT_TOKEN=
SINJAL_TEST_TELEGRAM_CHAT_ID=
SINJAL_TEST_DISCORD_WEBHOOK_URL=
SINJAL_TEST_SMTP_HOST=
SINJAL_TEST_SMTP_PORT=
SINJAL_TEST_SMTP_USER=
SINJAL_TEST_SMTP_PASS=
SINJAL_TEST_SMTP_FROM=
SINJAL_TEST_SMTP_TO=
SINJAL_TEST_SMTP_SECURITY=   # starttls (default) or tls
```

Tests that use them are guarded by a build tag (`manual`) and are skipped when the variables are unset. SMTP: `go test -tags manual -run TestManualSMTP ./internal/notify` sends one `[TEST]` message; Telegram: `-run TestManualTelegram`; Discord: `-run TestManualDiscord`.

## Fixtures

Fixtures must never contain real credentials or production URLs.
