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

Where each built scenario is covered is recorded in the two sections below: [Black-box integration tests](#black-box-integration-tests) (against the binary) and [In-process tests by milestone](#in-process-tests-by-milestone) (real SQLite, the real processor or handler).

## Black-box integration tests

`tests/integration` builds the real `sinjal` binary once per run and runs it with a scrubbed environment and a temporary data directory. These tests run as part of `make test` and `make test-race`, and are skipped with `go test -short`.

### M0 (`m0_test.go`)

`TestMilestone0`:

- boot from an empty data directory; database file (0600), `backups/` and `uploads/` created; migration 001 recorded
- `/healthz` and `/readyz` answer 200; `/` renders the app shell
- assets are hashed, immutable and gzip-capable
- SIGTERM exits 0
- a restart on the same directory neither migrates nor creates a backup

### M1 (`m1_test.go`, `auth_test.go`)

`m1_test.go`:

- startup refuses to run, and creates no key, when `master.key` is missing but encrypted data exists
- proxy trust (scenario 19): `X-Forwarded-For` changes the logged `client_ip` only when the peer is in `SINJAL_TRUSTED_PROXIES`
- initial setup: the setup link is logged once per start and rotates on restart; the old token is refused; the form creates the admin; then `/setup` is 404 and a restart logs no link
- sessions: an expired session is deleted at startup; `POST /logout` deletes the live session and clears the cookie; the token never reaches the logs

`auth_test.go` (M1-19), each test on its own server:

- login rate limit: 10 failures, then 429 even with the right password; the blocked attempt is not audited
- CSRF rejection on a signed-in POST (no token, wrong token, another session's token, cross-site `Sec-Fetch-Site`, foreign `Origin`), with the session surviving
- viewer mutation: 403 on every admin action with a valid CSRF token (scenario 18)
- an expired session no longer authenticates and its cookie is cleared
- a password change ends other sessions and rotates the current one
- re-authentication is enforced after the window, then unlocks after a correct password
- passkey registration and password-less sign-in through `internal/auth/passkeytest`; finish is not replayable
- TOTP codes are single-use, including the enrolment code

Each test asserts that the passwords, tokens and secrets it handled never reach the server log.

### M2 (`m2_test.go`, `m2_scenarios_test.go`)

`m2_test.go`: monitors seeded into the data directory while the server is stopped, against a local target.

- `TestRestartKeepsMonitorState`: a stored monitor is checked as soon as the server starts (failure, confirmation retry, DOWN). After a restart with the target still failing, a fresh check runs promptly and the monitor is DOWN since the original moment. After a restart with the target healthy, it is UP. Every SIGTERM exits 0.
- The same test follows the incident (scenario 8): one active incident from the first failed check, the same single incident after the restart, closed by the recovery with `detected`, `declared_down`, `recovered` as its events.
- `TestEventsStream`: `GET /events` redirects to the login when signed out. A signed-in stream receives `monitor.updated` with the monitor's id when a check has been stored. SIGTERM with the stream open ends it cleanly and exits without the shutdown grace period running out (bound 9 s: `net/http` waits up to 5 s for a connection that never sent a request, which Go's client sometimes leaves behind).

`m2_scenarios_test.go` (M2-19): one server run against one local target with a path per scenario; verdicts read from `check_results`.

- `TestMonitoringScenarios`:
  - 1: failure → confirmation retry → DOWN (two failed results, status and escaped body kept)
  - 2: failure → successful retry → UP, never DOWN (the state is polled throughout)
  - 3: DOWN → recovery → UP since the recovering check
  - 14: a 16 MiB body whose match lies past the 1 MiB cap fails `body_assertion`, and the target cannot finish writing (the check stopped reading)
  - 15: a redirect chain within the limit succeeds; an unfollowed 302 fails `http_status` with status 302; a loop fails `protocol` at 10 redirects
  - 16: a hanging target fails `timeout` near the 1 s timeout and sees its request cancelled
  - assertion failures (status, contains, does not contain, JSON value, invalid JSON), each with its own kind and a snippet; passing JSON assertions; no successful check stores a body
- `TestCreateMonitorThroughForm`: a monitor created through the real form (`POST /monitors`, bearer secret) is announced (`monitor.created`), checked at once with its secret, announced again, audited and shown on its page. After a confirmed delete it is gone with its results and secrets, announced as `monitor.deleted`. The token never reaches the log.

### M3 (`m3_scenarios_test.go`)

M3-13, `TestFlappingScenario` (scenario 4): a target that fails twice and succeeds, repeated, with a one-second interval. The fourth transition sets `flapping_since`; the log shows down, recovery, down, one `flapping` notice and the recovery suppressed by it; the incident events hold the suppression; the exit is clean.

Scenarios 5–7 stay at processor level (`internal/results`, see [M3](#m3-processor-uptime-history-charts-incidents)). Scenarios 5 and 7 are also run against the binary in M5-12.

### M4 (`m4_scenarios_test.go`)

M4-07, against the binary with one-second intervals set in the database:

- `TestMonitorTypeScenarios`:
  - TCP: an open local port is UP; a closed one is DOWN with `connect`
  - DNS through a local UDP responder: UP when the answer holds the expected value; DOWN with `dns_mismatch` (missing value and answer in the snippet) when not
  - ICMP to 127.0.0.1: UP where pinging is permitted, otherwise only the permission failure
- `TestICMPPermissionSurfaced` forces the second ICMP case. The binary runs under `unshare -Un` (an unprivileged user and network namespace: no `CAP_NET_RAW`, empty `ping_group_range`). Every result is `permission` with the hint naming both fixes, and the monitor is DOWN with an open incident for that reason. It skips where unprivileged user namespaces are disabled.
- `TestHeartbeatExpiryScenario` (scenario 20):
  - a heartbeat monitor created through the form answers with its push URL once (`no-store`)
  - its period is cut to 2 s + 1 s grace in the database
  - beats by path and by bearer token keep it UP
  - silence makes it DOWN no earlier than the period, with `heartbeat_missed` ("expected every 2s, grace 1s") and an open incident; a beat closes it
  - the detail page shows the heartbeat facts and not the token
  - regenerating issues a new token: the old one answers 404 and the new one 204
  - neither token reaches the log

### M5 (`m5_test.go`, `m5_scenarios_test.go`)

`m5_test.go`, `TestNotificationsAcrossRestart`: the built binary with a seeded channel, profile and route delivers one DOWN; a restart while DOWN sends nothing more; the recovery is delivered once; the timeline and the channel's health are recorded; no shutdown lost a notification.

`m5_scenarios_test.go` (M5-12): the built binary with one-second intervals; channels, profiles and windows seeded while it is stopped; a local target with a path per monitor; fake endpoints. Webhooks run over HTTP, Discord over a TLS `httptest` server, and SMTP over a minimal implicit-TLS server with the same certificate. Both TLS servers are trusted only through `SSL_CERT_FILE` handed to the binary, so nothing in the product is configured for the test.

- `TestNotificationScenarios`:
  - one DOWN however many failing checks; one reminder (`reminder_after_seconds` 2, marked once); the recovery payload with `duration_seconds`, `latency_ms` and severity info; the DOWN and reminder also by Discord and email
  - quiet hours covering now: with the bypass, DOWN sent and `recovery: quiet_hours` recorded with nothing sent; without it, `down: quiet_hours` with nothing sent
  - scenario 5: held with `down: maintenance`, announced once the window has ended and the monitor is still down (`notification_resumed`)
  - scenario 7: the child held with `down: parent`, announced once the parent recovered
  - used channels end healthy
- `TestProviderOutageScenario` (scenario 9, about 35 s: the real 30 s step): the endpoint fails the DOWN (health warning) and the recovery. The DOWN's retry is `dropped` once the incident ended and never delivered; the recovery's retry is sent; health is healthy; the timeline says both. Giving up after the fourth attempt stays with the dispatcher tests (it takes 12.5 min for real).
- `TestSimulationLeavesHistoryUntouched`: signed in, "Simulate incident" delivers `[TEST]` DOWN and RECOVERY with `"test": true` and leaves incidents, events, deliveries, results and the channel's health as they were, with one audit row.
- Telegram's API address is fixed in the binary, so `notify.TestTelegramDeliversEveryKind` sends every kind of message (DOWN, RECOVERY, reminder, TLS, `[TEST]`) through the real sender to a fake Bot API instead.

Stable over `-count=3`.

### M6 (`m6_scenarios_test.go`)

- `TestRetentionRollupScenario`: results 1, 10, 40 and 400 days old are seeded while stopped. The startup catch-up keeps yesterday's raw results, rolls the others into 5m, 1h and 1d with every result and failure counted, and records `last_retention_run`.
- `TestRetentionFailureScenario`: an abort trigger on the aggregate write. The failure is logged, all raw results are kept, no bucket is written and no run is recorded. Without the trigger, the next start rolls them.

### M7 (`m7_scenarios_test.go`)

Scenarios 17 and 19 for status pages (`TestStatusPagesRedactionAndAccess`, `TestStatusPageHostRouting`):

- four pages (public and mapped, password, authenticated, unlisted) over one monitor whose private host, address, URL secret, failure message, snippet and ids must never appear
- every address (slug, token, mapped hostname) and format (HTML, `api.json`, `feed.xml`) is read and scanned
- password and authenticated pages answer their machine formats 401 without content
- an unlisted page has no slug address
- a spoofed `X-Forwarded-Host` from an untrusted peer shows the app, not the page; a trusted proxy's is believed, and the mapped host serves nothing but the page

## In-process tests by milestone

### M3: processor, uptime, history, charts, incidents

Processor level (M3-02 to M3-05; `internal/results`, real SQLite, the real processor, scripted results with fixed times):

- incident lifecycle (`incidents_test.go`) and notification intents (`intents_test.go`)
- flapping (`flapping_test.go`): entry at the fourth transition, the window edges, suppression while flapping, both exits, an exit that coincides with a transition, restarts mid-window and while flapping, pauses
- parent suppression (`parent_test.go`): the child's DOWN held back and its incident marked; a pending parent holding nothing; parent and child in one batch; the held DOWN decided once after the parent recovers, also across restarts and within one batch; suppressed by flapping instead when the child flaps
- maintenance (`maintenance_test.go`): the DOWN held back and decided after the window, also when the window ends within the batch; recovery inside a window; `maintenance_overlap` at opening and at close; windows that hold nothing; a daily window in the instance time zone

Window evaluation is table-tested in `internal/maintenance`: one-time, daily, weekly mask, across midnight, merged occurrences, DST in Europe/Belgrade and America/New_York (kept local time, skipped and repeated hours, windows over the change), a whole year of a daily window.

Uptime (M3-08):

- the formula, table-tested in `internal/incident`: clipping to creation and now, active incidents, overlaps, paused time, maintenance inside and outside observed time, no data, truncation, whole seconds
- against real SQLite in `internal/store`: raw ≠ adjusted with an excluded window, a parent-suppressed incident counted, windows of other monitors ignored, a monitor created paused, the index plans

History (M3-09):

- range parsing (`internal/history`): every preset, calendar days across DST, custom ranges and each rejection
- `store.LatencyHistory` on real SQLite: summary against a hand computation, failures excluded from latency, exclusive end, empty and failures-only ranges, p95 nearest rank for n = 1, 2, 19, 20, 21, 100, 101, bucket bounds and empty buckets, the index plan without a sort
- since M6-05, the union with rolled buckets: a range over raw, 5m, 1h and 1d against a hand computation including the approximate p95 and the series placement; rolled-only ranges; a bucket starting before the range left out; raw-only and failures-only buckets staying exact; rolling a range keeping its counts, extremes and average; the aggregate read on the key

Charts (M3-10):

- the timeline's segments: precedence paused > down > maintenance > up, no data before creation, nothing after now, widths and titles
- the chart JSON: gaps as null, overlays clipped, active outage to now, marks only for starts in range
- the sparkline geometry and label, and the summary sentence
- the History tab end to end: figures, data attributes, scripts only with data, an invalid custom range explained, the header sparkline
- the CSP test with a populated tab
- in Chromium at the time: hover values, the four themes of the time redrawing, 390 px

Range selector and header uptime (M3-12, `internal/web/charts_test.go`): every preset as a link and only the shown one current; the custom range kept in the form and without a current preset; the Overview tab with the 24-hour chart and no selector whatever the query says; the header uptime raw and adjusted (seeded outage inside an excluded window); the list without it; the selector in the CSP test.

Incidents (M3-11):

- incident changes at processor level (`internal/results/changes_test.go`): opened once, closed on recovery, updates for held and released DOWNs, nothing for a blip
- the pause close announced by `Engine.Pause` (`TestPauseAnnouncesTheClosedIncident`) and the hub frame with two ids
- the store readers and their query plans (`internal/store/incident_read_test.go`)
- the pages end to end (`internal/web/incidents_test.go`): list order for admin and viewer, fragment per monitor, the Incidents tab, timeline, notes (refusals, escaping, audit row, `incident.updated` on a real stream), viewer 403

### M5: notifications

Dispatcher (M5-08; `internal/dispatch`, real SQLite, a fake sender, millisecond retry delays):

- a DOWN rendered from the incident and delivered with its row, timeline entry, channel health, claim and announcements; the recovery message
- routing by the severity matrix; a notice without an incident
- nothing sent without a profile, a route or an enabled channel, and nothing claimed
- suppressed intents never delivered, then the catch-up once
- quiet hours per severity, the critical bypass on and off, in the instance time zone, with the suppression event
- one DOWN per incident, also with a second dispatcher on the same database; a new incident gets its own
- the retry ladder to success (rows, health, one timeline entry, the same message every time) and to failure (four attempts timed by the waits, health `failed`, the last error on the timeline); the default ladder and the rate-limit wait bound
- a stale DOWN dropped once the incident ended, while the recovery still goes
- a channel deleted mid-retry; never more than four sends at once; a full queue dropping without blocking
- a send cut off by shutdown recording nothing while its claim stands; a send that finished during the shutdown recorded
- a TLS warning from the monitor's row; an unreadable configuration given up with a readable error
- `pipeline_test.go`: the real engine against a failing then healthy target, through `notify.Send` to a local webhook: the `monitor.down` and `monitor.recovered` payloads, the rows and the health
- store (`internal/store/deliveries_test.go`): target with and without a profile, routes without disabled channels, facts and latency around an outage, the claim once, health transitions and rows per attempt, a deleted incident or channel

Outage reminder (M5-09):

- processor level (`internal/results/reminder_test.go`, real SQLite, scripted results): decided on the first check at or after the duration, once, not again after a restart, the next incident getting its own; nothing without a profile, without a duration, or when the incident ends first, and nothing marked then; suppressed by flapping (with the `reminder: flapping` event) and by maintenance, and not sent later; a duration shortened mid-incident applies
- `internal/dispatch` (`TestReminderMessage`): the message with the duration and reason, delivered through quiet hours by the critical bypass, with its timeline entry
- `incident.Suppression` covers the new kind in all eight combinations

TLS expiry warnings (M5-10):

- `incident.CrossedThresholds` and `DaysLeft` table-tested: the day boundaries, the last day, an expired certificate, unsorted and empty lists
- processor level (`internal/results/tls_test.go`): 30 then 14 days each warned once, on failed checks too, nothing again after a restart; a renewed certificate warned afresh with the old rows gone; a certificate first seen with 5 days left gives one warning and three rows; nothing with warnings off or for a TCP monitor; a warning suppressed by maintenance stays recorded and is not sent after the window

Notifications UI (M5-11):

- store (`internal/store/profiles_test.go`): round trip with routes in channel-name order; update replacing routes and clearing quiet times; a deleted channel leaving the route; delete keeping the monitor without a profile; the validation table (name empty, long, taken; quiet times and an empty window; reminder bounds; unknown channel and severity)
- pages (`internal/web/profiles_test.go`):
  - profile create/edit/delete with the matrix, quiet hours and reminder saved and shown back; the list in words; the delete prompt counting monitors; audit rows
  - every problem of a bad form at once (422)
  - empty states for no channel and no profile; viewers without change links; the live fragment; every profile and test route 403 for a viewer
  - "Send test notification" against a local webhook: a redirect to the edit page showing the outcome, whose reload sends nothing; the `[TEST]` `monitor.down` with its header, a `test` delivery row, health healthy, the `notification.channel_updated` event; a failing endpoint shows the error and leaves health failed; the secret in neither page nor log
  - "Simulate incident": DOWN to the critical, RECOVERY to the info channel, `"test": true`, nothing to a disabled channel, a failure shown, the quiet-hours note, no row in incidents, events, deliveries or results and no health change, the audit row, a profile routing nothing
  - the monitor form's profile picker: offered, saved, shown on edit and on the Configuration tab, an unknown id refused on its field, cleared
- templates: the new pages in the CSP test, the profile and channel forms in the label test

### M6: retention

- bucket math, table-tested in `internal/history` (`bucket_test.go`): UTC alignment on a DST day, exact nearest rank, `FromRaw` and `Merge` against hand computations, failures-only buckets without latency, merges of merges keeping counts, extremes and average, `ApproxP95` weights and boundaries
- the rollup in `internal/retention` on real SQLite (`retention_test.go`): each tier's cutoff edge, values by hand, a year-old day chained into one daily bucket, a second run doing nothing, one transaction per day of data with gaps skipped, a stopped run resumed
- **Scenario 12** (`scenarios_test.go`, `TestScenario12SimulatedYear`): 425 days of results every 20 minutes, one day at a time, rolled after each day as the daily job would.
  - After every run each tier holds exactly its age range (raw from 7 days, 5m 7–30 days, 1h 30–365 days, 1d beyond).
  - Every 25 days the stored counts, failures, extremes and latency sum equal what was inserted; at the end every tier holds the expected number of buckets.
  - From day 380 to 424 the pages in use stay flat (mutation-checked: an hourly tier that never rolls grows the file by 59 pages and fails).
  - About 38 s under the race detector, which is why the interval is coarse.
- **Scenario 13** (`TestScenario13FailureKeepsSources`): a trigger makes one statement of a step fail (bucket write, raw deletion after the bucket write, 5m deletion after the hourly write, hourly write). The run reports it, nothing of the failed step changed, and the next clean run ends where a run that never failed does.
- the daily runner in `internal/jobs` (`daily_test.go`): schedule and overdue tables incl. Belgrade DST days, catch-up, a failed rollup not recorded, `PRAGMA optimize` after a run and not after a failure
- long-range reads are listed under [M3](#m3-processor-uptime-history-charts-incidents) "History"

### M7: status pages

In process (`internal/web/m7_test.go`, `publicfeed_test.go`, `api_test.go`): the same matrix as the M7 black-box test, with unlocked and signed-in access; forged, foreign and outdated page cookies; query and bearer tokens that open nothing; POST to the machine formats; and no admin API on a mapped hostname.

Mutation-checked: serving a locked page's JSON, or letting an anonymous request read an authenticated feed, fails them.

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
- the real `sinjal` binary is built once per run; each test boots it on a temporary data directory and drives it over HTTP
- no screenshots-as-assertions; assert on DOM text, attributes and status labels

Built (M8-12), in `tests/browser/browser_test.go`:

- `TestLogin`: setup, wrong password, sign in, sign out
- `TestCreateHTTPMonitor`
- `TestSSETransition`: a monitor going down swaps the detail header and column row over SSE, checked without a reload
- `TestThemeSwitch`: applied on save, kept across pages and in a new browser
- `TestStatusPageModes`: one page through public, password, authenticated and unlisted, opened each time from a fresh browser context; the monitor's own name and target never shown

The backup/restore confirmation test follows the backup UI (M9-01).

## Benchmarks

See `docs/18_PERFORMANCE.md`.

## Race testing

Run the Go race detector in development/test environments where feasible (`make test-race`; CI runs it).

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

Tests that use them are guarded by a build tag (`manual`) and are skipped when the variables are unset:

- SMTP: `go test -tags manual -run TestManualSMTP ./internal/notify` sends one `[TEST]` message
- Telegram: `-run TestManualTelegram`
- Discord: `-run TestManualDiscord`

### Runbook (M5-14)

1. Fill `.env` (git-ignored) with the variables above and load it: `set -a; . ./.env; set +a`.
2. Senders alone: `go test -tags manual -count=1 -v -run 'TestManual(SMTP|Telegram|Discord)' ./internal/notify`. Each sends one `[TEST]` message; a skipped test means its variables are unset. Check that each message arrived with the `[TEST]` title and the "simulated" first line.
3. Through the application: `make dev`, create the three channels from the same accounts (Notifications, Channels), press "Send test notification" on each. Expected: the message arrives and the channel's health becomes Healthy with a last-success time. A deliberately wrong secret must show the sender's one-line error and Failed or Warning health, with no secret in it.
4. Create a profile (info to Discord, warning and critical to Telegram and SMTP), press "Simulate incident". Expected: a `[TEST]` DOWN along the critical routes and a RECOVERY along the info routes arrive, and neither the incident list, history nor the delivery log gained a row.
5. Record the date, the three results and any surprise in `docs/IMPLEMENTATION_CHECKLIST.md`.

## Fixtures

Fixtures must never contain real credentials or production URLs.
