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

Milestone 2 (`m2_test.go`), monitors seeded into the data directory while the server is stopped, against a local target: a stored monitor is checked as soon as the server starts (failure, confirmation retry, DOWN); after a restart with the target still failing a fresh check runs promptly and the monitor is DOWN since the original moment; after a restart with the target healthy it is UP; every SIGTERM exits 0. Event stream: `GET /events` redirects to the login when signed out; a signed-in stream receives `monitor.updated` with the monitor's id when a check has been stored; SIGTERM with the stream open ends it cleanly and exits at once, without the shutdown grace period running out.

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
```

Tests that use them are guarded by a build tag (`manual`) and are skipped when the variables are unset.

## Fixtures

Fixtures must never contain real credentials or production URLs.
