# Implementation Checklist

Claude Code should update this file as milestones are completed.

## Milestone 0
- [x] repository foundation
- [x] config
- [x] SQLite
- [x] migration runner
- [x] health endpoints
- [x] base UI
- [x] theme scaffolding

## Milestone 1
- [x] password auth
- [x] sessions
- [x] CSRF
- [x] encryption key
- [x] secret envelope
- [x] passkeys
- [x] TOTP
- [x] viewer role

## Milestone 2
- [ ] monitor model
- [ ] HTTP config
- [ ] scheduler
- [ ] workers
- [ ] result processor
- [ ] HTTP checks
- [ ] assertions
- [ ] TLS metadata
- [ ] monitor UI
- [ ] SSE

## Milestone 3
- [ ] incidents
- [ ] flapping
- [ ] dependencies
- [ ] maintenance
- [ ] charts
- [ ] uptime

## Milestone 4
- [ ] TCP
- [ ] ICMP
- [ ] DNS
- [ ] heartbeat

## Milestone 5
- [ ] SMTP
- [ ] Telegram
- [ ] Discord
- [ ] webhook
- [ ] profiles
- [ ] quiet hours
- [ ] retry
- [ ] channel health

## Milestone 6
- [ ] retention
- [ ] 5m rollup
- [ ] 1h rollup
- [ ] 1d rollup

## Milestone 7
- [ ] multiple status pages
- [ ] access modes
- [ ] groups
- [ ] branding
- [ ] hostname mapping
- [ ] JSON/feed

## Milestone 8
- [ ] overview polish
- [ ] command palette
- [ ] shortcuts
- [ ] density modes
- [ ] mobile
- [ ] accessibility
- [ ] four final themes

## Milestone 9
- [ ] config export/import
- [ ] full backup
- [ ] restore
- [ ] automatic local backups
- [ ] system diagnostics

## Milestone 10
- [ ] load testing
- [ ] 1,000 monitor benchmark
- [ ] contention tests
- [ ] graceful shutdown
- [ ] payload/image size checks
- [ ] security review

## Milestone 11
- [ ] README
- [ ] docs
- [ ] release script
- [ ] SBOM
- [ ] checksums
- [ ] signing
- [ ] release dry run

## Implementation log
- M0-01: Go module `github.com/drilonrecica/sinjal` (go 1.27); chi and modernc sqlite pinned; templ pinned via `tool` directive. `cmd/sinjal/deps.go` is temporary and is deleted once chi/sqlite are imported for real.
- M0-02: `cmd/sinjal` entrypoint with testable `run()`; `serve` (default) and `version`; `serve` is a stub until M0-10.
- M0-03: `Makefile` (dev, generate, fmt, lint, test, test-race, bench, build, reset-dev-db, release-local stub); dev data in git-ignored `./.dev-data`.
- M0-04: `internal/config` environment loader (strict, reports all errors, rejects unknown `SINJAL_*`); `serve` fails fast on bad config; tzdata embedded; env table in `docs/15_CONFIG_BACKUP.md`.
- M0-05: `internal/secret.String` (redacts via fmt/slog/JSON) and `internal/logging` (slog text/json, `Sub` adds `subsystem`); `serve` logs a startup line; conventions in `docs/30_LOGGING_ERROR_HANDLING.md`.
- M0-06: `internal/datadir.Init` creates data dir, `backups/`, `uploads/` (0700, only for dirs it creates) and proves writability with a probe file; errors name the path and hint at ownership.
- M0-07: `internal/db` (`Open`: single-connection writer + `query_only` reader pool, DSN pragmas; `Retry`: busy backoff 25/100/250/1000 ms with typed `BusyExhaustedError`).
- M0-08: `internal/db.Migrate` (embedded `migrations/NNN_*.sql`, one tx each, `schema_migrations`, refuses newer DB, mandatory `VACUUM INTO` pre-migration backup except for a brand-new DB, failure-injection tests).
- M0-09: `001_foundation.sql` creates `system_settings` only; guard test keeps the embedded migration set contiguous. The `.gitkeep` placeholder is gone.
- M0-10: `internal/web` (chi router; request-ID, access-log and panic-recovery middleware; server timeouts; bounded graceful shutdown) and `serve` wired end to end: config → data dir → DB → migrations → listen → serve. The access log records the route pattern, never the raw path/query. `cmd/sinjal/deps.go` removed. Note: chi skips middleware on a router with no routes; resolved when M0-11 adds `/healthz`.
- Fix (M0-07): `db.Open` pre-creates `sinjal.db` with mode 0600 so the -wal/-shm files are owner-only too.
- M0-11: `internal/web/health.go` — `/healthz` (`SELECT 1` on the read pool, 2 s) and `/readyz` (flag set after migrations), plain text, no-store, HEAD supported, no internals in bodies; wired in `serve`. Middleware now runs on every request because a route exists.
- M0-12: `web/templates/layout.templ` base layout; generated `*_templ.go` committed; `make check-generated` (hash-based stale check, part of `make lint`); templ is now a runtime dependency.
- M0-13: `web/static` embedded via `web.Static`; `internal/assets` serves content-hashed URLs (immutable cache, ETag/304, gzip precompressed at startup, explicit mime table); htmx 2.0.11 (0BSD) vendored with provenance header; `/static/*` route helper `web.RegisterStatic`.
- M0-14: `web/static/css/tokens.css` (full 05 token model for Carbon/Paper/Midnight/Terminal, density factor) and `base.css` (reset, system font stacks, spacing/type scale, focus ring, reduced motion); `templates.Page` + `NewPage` (validated theme/density → `data-theme`/`data-density`, `color-scheme` meta); `/static/*` wired into `serve`. `web/tokens_test.go` enforces token completeness and WCAG contrast for every theme. Fonts stay deferred to M8-01.
- M0-15: app shell (`web/templates/shell.templ`, `shell.css`): skip link, brand header, `nav[aria-label=Primary]` with the 7 sections and `aria-current` + bar marker, `main#main`; placeholder pages via `web.RegisterPages`; `render` helper buffers output so template errors become a clean 500; htmx loaded with `defer`. Checked in a browser at desktop (Carbon) and 390 px (Paper, compact); no console errors except the missing favicon. No icons/collapse/drawer yet (M8).
- M0-16: `make lint` = gofmt, go vet, `go tool staticcheck` (dev tool pinned in go.mod), templ format check, stale-generation check. Each check was seen to fail on a deliberate violation. staticcheck found nothing in M0-01..15.
- M0-17: `.github/workflows/ci.yml` — check-only (push to master + manual), `contents: read`, actions pinned by SHA (checkout v4.2.2, setup-go v5.6.0), runs `make lint` and `make test-race`. YAML parsed and constraints asserted locally; a fresh clone passes both steps. First real run happens on the next push. The older `close-prs.yml` (`actions/github-script@v7`) is not SHA-pinned and was left untouched.
- M0-18: `tests/integration/m0_test.go` — black-box test that builds and runs the real binary: empty data dir boot, DB/migration/permissions, health endpoints, shell render, hashed/immutable/gzip assets, SIGTERM exit 0, restart with no migration or backup. Seen to fail when `/readyz` output is changed.
- M0 gate (agent part, 2026-10-06): exit criteria verified against the built binary — starts from an empty data dir, DB created (0600) with `schema_migrations` = [1], `/healthz` `/readyz` 200, `/` renders the shell, only a listening socket is open (no outbound connections, no external services). Backend gate: `make lint` (gofmt, vet, staticcheck, templ fmt, stale generation) clean; `go test -race -count=1 ./...` green incl. the black-box integration test; `govulncheck` clean; no benchmarks exist yet. `make dev` / `make reset-dev-db` exercised. **Owner smoke test of `make dev` still pending.**
  - Baseline for later regression checks (linux/amd64, idle, empty DB): binary 11.5 MB (`CGO_ENABLED=0`, stripped), cold start to `/readyz` 37 ms, RSS 15 MB, 8 threads, ~0% CPU, shell HTML 1.4 KB served in <1 ms. Budgets in `18_PERFORMANCE.md`: <50 MB RAM, <1 s start, <50 ms HTML.
  - Not covered by the M0 gate (no code yet): migration from a real prior-release fixture (only fake-FS upgrade tests exist), hot-path benchmarks, container image size, arm64 build.
- M1-01: `002_auth.sql` — `users` (with theme/density/sidebar prefs), `sessions`, `passkeys`, `audit_events` + indexes, identical to `spec/schema.sql` (drift guard test). Tests cover columns/defaults, CHECK/UNIQUE/FK constraints, cascade on user delete (audit rows kept with NULL user), AUTOINCREMENT ids, and a real 001→002 upgrade with data and a pre-migration backup. M0 integration test now derives the expected schema versions from the migration files. M0-G treated as signed off when M1-01 was requested.
- M1-02: `internal/vault.LoadOrCreate` — `master.key` (32 raw bytes, 0600, temp file + fsync + hard link so it is never overwritten); refuses loose permissions, wrong size, non-regular or unreadable files; when the key is missing it is generated only if no `*_enc` column holds data, otherwise startup fails with restore guidance. Wired into `serve` after migrations. Unit tests plus integration: key 0600 on first boot, unchanged on restart, and refusal to start (no key created) when the key is missing but encrypted data exists. Mutation-checked: disabling the guard turns both tests red.
- M1-03: `vault.Key.Seal/Open` — v1 envelope `0x01 | nonce(12) | AES-256-GCM ct | tag`, nonce generated by `cipher.NewGCMWithRandomNonce`; AAD = version + length-prefixed table/column/row-id. Tests: round trip (incl. empty, after key reload from disk), every single-byte flip, each context field, AAD ambiguity, wrong key, unknown version, truncation, 10,000 unique nonces. Seal ≈ 0.3 µs / Open ≈ 0.2 µs for 64 B (i5-7500).
- M1-04: `internal/auth` `HashPassword`/`VerifyPassword` — Argon2id m=19 MiB t=2 p=1 (OWASP baseline), PHC string, strict bounded parsing (`ErrInvalidHash`), constant-time compare, `needsRehash` on parameter/length change (consumed by M1-10 login), at most 2 concurrent hashes (~38 MiB transient cap). Benchmarked ~27 ms / 19 MiB per op on i5-7500; documented in `13_AUTH_SECURITY.md`. Known vectors cross-checked with OpenSSL. `golang.org/x/crypto` v0.57.0 added (argon2 only, per `40_DEPENDENCIES.md`). govulncheck: 0 affecting; it lists module-level GO-2026-5932 (`x/crypto/openpgp`, not imported, no fix).
- M1-07: `internal/web/proxy` — resolves client IP / scheme / host once per request (router middleware after request ID). `X-Forwarded-For/Proto/Host` honoured only when the TCP peer is in `SINJAL_TRUSTED_PROXIES`; XFF walked right to left skipping trusted hops (malformed entry stops the walk); rightmost proto/host, validated; `Forwarded` ignored; 4in6 normalised. `proxy.ClientIP/IsHTTPS/Host` are the only accessors (trust nothing without the middleware). `web.NewRouter` takes the trusted prefixes; access log gains `client_ip`. Unit table tests plus black-box scenario 19 (`TestProxyTrust`: no proxies / untrusted peer / trusted peer). Implementation order agreed with owner: M1-07 before M1-05/M1-06, which need these helpers.
- M1-05: initial setup per P0-10. `auth.SetupToken` (128-bit, memory only, constant-time check, discarded on success) logged once at WARN as `Initial setup: <base>/setup?token=…` while no admin exists. `/setup` (GET form, POST create) is 404 once an admin exists (DB checked per request); wrong token → generic 403, 10 failures / 15 min per client IP → 429 (`internal/ratelimit`, bounded to 1024 keys, reused by login). `auth.CreateAdmin`: validation, hash outside the tx, conditional `INSERT … WHERE NOT EXISTS` + `setup.admin_created` audit row in one tx (16-way race test → exactly one admin). Password policy (owner): 12 chars min, 1024 bytes max. `internal/ids.New` (128-bit hex) for TEXT ids. Minimal `templates.Setup`/`AuthMessage` + `css/auth.css`; themed polish stays in M1-18. Success redirects to `/login` (404 until M1-10). Checked in a browser (Carbon); integration test covers rotation, refusal of the old token, creation and closure.
- M1-06: `auth.Sessions` — 32-byte token (base64url cookie, SHA-256 stored), 30 d absolute expiry, `reauthenticated_at` set on create/rotate, `last_seen_at` written at most every 5 min (conditional update, failure only logged), rotate/delete/delete-others/delete-all/delete-expired; `auth.SetPassword` changes the hash and revokes other sessions in one tx. Web: `__Host-sinjal_session` + Secure via `proxy.IsHTTPS` (direct TLS or trusted proxy), else `sinjal_session`; HttpOnly, SameSite=Lax, Path=/; `LoadSession` middleware (no enforcement until M1-11; stale cookie cleared); `POST /logout`. Expired-session cleanup at startup + every 24 h (moves to M6-04). Unit tests with a fixed clock (throttle, expiry boundary, disabled user, rotation, scoped deletion, cookie attributes for HTTP / TLS / trusted / spoofed proxy) and a black-box test (startup cleanup, logout, no token in logs). No login yet (M1-10), so sessions are only created by tests.
- M1-08: CSRF (`internal/web/csrf.go`): origin check (`Sec-Fetch-Site`, else `Origin` vs the proxy-resolved scheme/host) on every unsafe request, plus a per-session synchronizer token `HMAC-SHA256(Derive("sinjal csrf v1"), session id)` from `X-CSRF-Token` or `_csrf` when signed in; anonymous POSTs get the origin check only. `vault.Key.Derive` (HKDF-SHA256). Route wiring moved from `main` into `web.Routes` (session group = `LoadSession` + CSRF; health/static/machine endpoints outside). `Page.CSRFToken` → `hx-headers` on `<body>` + `templates.CSRFField`; minimal "Sign out" form in the shell. Tests: token binding, header/form/query, fetch metadata, Origin (incl. trusted proxy), rendering; mutation-checked (disabling either check turns tests red). Integration: logout without token → 403, with the token scraped from a page → 303.
- M1-09: `middleware.SecurityHeaders` (first after request ID, so every response incl. 404/panic/static): strict self-only CSP without `unsafe-*`, nosniff, `Referrer-Policy: same-origin` (setup keeps `no-referrer`), `X-Frame-Options: DENY`, COOP same-origin, Permissions-Policy (WebAuthn left at default). No inline theme script exists (theme is server-rendered), so no hash. `htmx-config` meta disables htmx's injected styles, eval and script tags; indicator CSS moved to `base.css`. Guard test renders every page and fails on CSP-blocked inline code. Checked in Chromium: no CSP violations (only the known favicon 404). No HSTS (reverse proxy's decision).
- M1-10: login. `auth.Authenticator.Login`: generic `ErrInvalidCredentials`, one Argon2 verification on every path (dummy hash for unknown/disabled/password-less accounts; verified by counting calls), rehash on outdated params, audit `auth.login_succeeded`/`auth.login_failed` with client IP (never the attempted login). `/login` GET/POST with 401 generic failure, per IP+login limiter (10/15 min, 4096 keys, checked before hashing), previous session deleted on login, 303 to `safeNext(next)` (open-redirect table test). Audit insert helper shared with setup until M1-17. Integration: setup → wrong password 401 → login → logout with CSRF → old cookie dead; audit counts; no credential or token in logs.
- M1-11: authorization (`internal/web/authz.go`). `RequireAuth` (303 to `/login?next=` for pages, 401 otherwise, `HX-Redirect` for htmx) and `RequireAdmin` (403 for viewers). `web.Routes`: setup/login/logout public in the session group; pages behind `RequireAuth`; an admin group where every state-changing app route goes. Pages use the user's theme/density. `TestRouteTableGuards` walks the real table (`chi.Walk`) and probes every route anonymously and as a viewer with a valid CSRF token; `TestRouteTableGuardsCatchOmissions` proves it reports unguarded routes. M0 integration test now signs in (setup → login) before checking the shell; anonymous `/` → 303 `/login`. Viewer account management stays M1-16.
- M1-12: re-authentication. `auth.ReauthWindow` (10 min) + `Session.RecentlyAuthenticated`; `RequireRecentAuth` (stale GET → `/reauth?next=<path>`, stale POST → same-origin Referer as `next`, htmx → `HX-Redirect`, fails closed without a session). `/reauth` (viewers too): password check via `Authenticator.Reauthenticate` (shared verify path, audit `auth.reauthenticated`/`auth.reauth_failed`, login limiter keyed IP+user), success rotates the session (old cookie and CSRF token invalid) and redirects to `safeNext`. Fix found by the test: audit row for a user deleted mid-session no longer violates the FK. TOTP/passkey options plug in with M1-13/M1-14. Browser check (Chromium): setup → login → re-auth → back to `/monitors` → sign out, no CSP violations; it surfaced an unrecognised `bluetooth` Permissions-Policy entry (fixed separately). curl: anonymous `/` → 303, cross-site and token-less logout → 403; no password in the server log.
- M1-13: TOTP (admins). `internal/auth/totp.go` on the standard library: RFC 6238 SHA-1 / 6 digits / 30 s / ±1 step, constant-time comparison, RFC 4226 and RFC 6238 vectors. Secret sealed in `users.totp_secret_enc`; new column `users.totp_last_step` (amended into the unreleased `002_auth.sql`; existing dev databases need `make reset-dev-db`) makes every code single-use through a conditional UPDATE. Enrolment is stateless: the pending secret rides in the form, encrypted, user-bound, 10 min; shown as QR (`rsc.io/qr` v0.2.0, PNG data URI), setup key and `otpauth://` link. Login is two-step (`POST /login/totp` with a 5-minute encrypted challenge; success audited only after the code; wrong codes limited per account). `/reauth` asks for password and code. Minimal `/settings/authentication` page (admin group; setup/enable/disable behind `RequireRecentAuth`, its first production use); every change deletes other sessions and rotates the current one. `App.Vault` carries the master key into the route table. Mutation-checked: dropping the replay condition or the recent-auth guard turns tests red. Browser check (Chromium): enrol → wrong code → sign in with code → used code refused → re-auth → disable, no CSP violations, no secret in the server log. Binary 12.6 MB. Two chance failures in older tests fixed on the way (CSRF wrong-token, envelope 1-byte plaintext).
- M1-14: passkeys (admins). `go-webauthn` v0.18.2 behind `internal/auth/passkey.go`: RP ID and origin from `SINJAL_BASE_URL` only; unusable URLs (unset, IP, `http` off localhost) turn passkeys off with a logged and displayed reason; a request from another origin gets a 400 naming both addresses. Owner decision: a passkey signs in alone, so resident key and user verification are required. Ceremonies as JSON begin/finish pairs for sign-in (`/login/passkey/*`), re-auth (`/reauth/passkey/*`) and registration (`/settings/authentication/passkeys/*`, admin + recent auth); revoke per passkey. State in a bounded in-memory map (256 entries, 5 min, single-use) keyed by an HttpOnly SameSite=Strict cookie. New column `passkeys.backup_eligible` (amended into `002_auth.sql`; dev databases need `make reset-dev-db`). Sign count stored, regression refused and audited; multiple labelled credentials (max 20); add/remove revoke other sessions and rotate the current one. `web/static/js/passkey.js` (1.8 KB gzip, base64url ↔ `navigator.credentials`, no inline code). Tests drive real ceremonies through a software authenticator (`internal/auth/passkeytest`); seven guards mutation-checked (UV, ceremony binding, clone warning, duplicate credential, origin check, single-use state, recent auth). Browser check (Chromium, CDP virtual authenticator): add → duplicate refused → sign in → re-auth → wrong-address message → remove, no console errors. Binary 14.9 MB (+2.3 MB); govulncheck: 0 affecting. **A real device is still the owner's M1 gate item.**
- M1-15: account recovery CLI. `sinjal reset-admin [--login X] [--remove-passkeys]` (P0-11 decision a). `auth.ResetAdmin` does everything in one transaction (new random password, TOTP cleared, sessions deleted, passkeys only with the flag, audit `admin_reset_cli`); the target admin is chosen inside the transaction, ambiguity and viewers are rejected, failed resets write nothing. No migrations, no master key, and no database is created when the path is wrong. Password alone on stdout. Tests: unit (clearing, viewer untouched, selection errors, no audit on failure), command (flags, exit codes, no DB creation) and an integration test against a running server (old session and password die, new password works, password absent from the log). Mutation-checked: removing the session delete turns `TestResetAdmin` red.
- M1-16: viewer accounts. `auth.CreateViewer` / `SetViewerDisabled` / `ListViewers` / `ChangePassword` (`internal/auth/users.go`); admin-only create, disable and enable under Settings → Authentication behind recent re-authentication; own password change at `/account/password` for admins and viewers (`viewerMutations`), which deletes the other sessions and rotates the current one. Disabling deletes the viewer's sessions in the same transaction; admins cannot be disabled; logins are unique case-insensitively. Audit: `user.viewer_created|disabled|enabled`, `auth.password_changed`. Functional markup only (list, add form, password page); the themed polish is M1-18. Mutation-checked: removing the session delete or the `role = 'viewer'` guard turns tests red. `TestRouteTableGuards` covers the new routes; the CSP template test renders both new views.
- M1-17: audit log. New `internal/audit` (event constants, `Write` for a DB or transaction, `Record` with busy retry, `List` with keyset paging); every auth event moved onto it and `internal/auth/audit.go` removed, event names unchanged (`admin_reset_cli` stays as the spec names it). `GET /settings/system` lists the log read-only (admin only, 50 per page, UTC, `css/audit.css`), covered by `TestRouteTableGuards`, the CSP template test and its own tests (newest first, paging while events arrive, bad cursor, viewer 403, no password on the page). Mutation-checked: mounting the page outside `RequireAdmin` fails the viewer test. Nothing deletes audit rows yet; retention is M6-04.
- M1-18: auth UI. Labels and inline errors checked by template tests (every input labelled; the generic failure alert is tied to its fields on login, TOTP and re-auth), pre-login card, settings navigation by role (`Page.Admin`), danger styling for access-removing actions, 2.75 rem touch targets, new "Sign out other sessions" (`auth.SignOutOtherSessions`, audit `auth.sessions_revoked`, re-auth required, viewers allowed and listed in `viewerMutations`). `/account/password` is now "Your account". No new colour: only tokens, so `TestThemeContrast` covers all four themes. Browser-checked in Chromium on the built binary (desktop and 390 px, no CSP violation). Not done here: Appearance, General and the other settings pages; M1-19 adds the auth integration tests.
- M1-19: auth integration tests. `tests/integration/auth_test.go` drives the real binary: login rate limit, CSRF rejection (5 variants), viewer mutation 403 (scenario 18, incl. passkey registration and admin pages), session expiry on request, password change revoking other sessions, re-auth enforcement and rotation, passkey registration + sign-in via `passkeytest` (server started on `localhost:PORT` with matching `SINJAL_BASE_URL`; new `startAt` helper), TOTP single use incl. the enrolment code; secrets checked absent from the server log in every test. Setup token, login/logout and wrong password stay in `m1_test.go`. Session timestamps are aged through the DB while the server runs (no clock override). Mutation-checked: removing `RequireAdmin`, the TOTP step condition, `setPasswordTx`'s session delete, the `expires_at` check or the recent-auth check each turns the matching test red. No production code changed. Integration suite ≈3 s.
- M1 gate (agent part, 2026-10-06): backend gate on `3bf2ffd` — `make lint` clean, `go test -race -count=1 ./...` green (integration suite 10 s under race), `govulncheck`: 0 affecting (the one module-level advisory is the known `x/crypto/openpgp`, not imported). Exit criteria against the built binary (15.0 MB): setup → login → TOTP enrolment over curl works; `master.key` 0600 / 32 bytes; `users.totp_secret_enc` is a v1 envelope (49 bytes, first byte 1) and does not contain the plaintext secret; the server log holds no password, TOTP secret or code. Security-gate items map to tests: no secret logging (every `auth_test.go` test, `TestLoginLogout`, `TestInitialSetup`), viewer/admin boundary (`TestViewerCannotMutate`, `TestRouteTableGuards`), CSRF (`TestCSRFRejection`, `csrf_test.go`), trusted proxy (`TestProxyTrust`, `internal/web/proxy`). Not applicable yet: status-page redaction, backup extraction (later milestones). **Owner still to do: add and sign in with a passkey on a real device.** M2 stays blocked on that.
- M2-01: `003_monitors.sql` — `notification_profiles` (full spec table, FK target; channels and routes stay in M5), `monitors`, `http_monitor_config`, `monitor_secrets`, `monitor_pauses` (partial unique index: one open pause per monitor), `tags`, `monitor_tags`, `check_results` and their indexes, generated from `spec/schema.sql`. The tcp/icmp/dns/heartbeat config tables are left to M4. `monitors_migration_test.go`: defaults, CHECKs (no DEGRADED), unique names, open-pause rule, cascades and SET NULL, AUTOINCREMENT ids, `EXPLAIN QUERY PLAN` for the `check_results` hot query (index, no temp b-tree), a real 002→003 upgrade with a pre-migration backup, and a spec drift guard.
- M2-02: monitor store. New `internal/store` with plain functions (no interfaces): `CreateHTTPMonitor` / `UpdateHTTPMonitor` / `DeleteMonitor` (one transaction each via `db.Retry`; config and tags move with the monitor; delete prunes unused tags), `GetMonitor`, `ListMonitors`, `GetHTTPConfig`, tag functions (case-insensitive upsert via `setTags`, `TagsByMonitor` for list pages) and secrets (`SetSecret` / `GetSecret` / `SecretNames` / `DeleteSecret`, sealed with `vault.Key` bound to table, column and `monitorID/name`). Defaults applied in one place (`applyDefaults`); a disabled monitor is created `paused`. Tests on real SQLite: defaults, validation with rollback of the whole create, an edit keeps state and check history, cascades, a copied secret envelope refuses to open, and the list/get/config reads still work with `monitor_secrets` dropped (proves they never touch it). No audit events or status-expression validation yet: M2-04 owns the service layer.
- M2-03: status expression parser. `internal/monitor/status.go`: `ParseStatus` → `StatusExpr` (`Match`, canonical `String`). Accepts `200`, `200-299`, `200,204` and mixed lists; strict: exactly three ASCII digits in 100-599, ranges low to high, no empty parts, signs, wildcards or unicode digits, at most 64 characters and 16 parts; spaces around parts are ignored. The zero value accepts nothing. Table tests for accepted and rejected forms plus `FuzzParseStatus` (no panics; accepted input round-trips through its canonical form with an identical match set; seeds run in `go test`, 2.8 M executions clean in a 20 s run). Mutation-checked: dropping the backwards-range check turns the rejection test red. Not wired into the store yet: M2-04 validates with it.
- M2-04: monitor validation. `store.FieldErrors` (field → message, every field checked in one pass) replaces the name/URL-only check; `CreateHTTPMonitor` / `UpdateHTTPMonitor` return it and write nothing. Rules in `internal/store/validate.go`: common (name, interval 10 s–24 h, 0 < timeout < interval, thresholds 1–100, retry delay ≥ 0, tags, parent ≠ self) and HTTP (http/https URL ≤ 2048 with host and no userinfo, GET/HEAD/POST with body only on POST ≤ 64 KiB, status via `ParseStatus` stored canonical, body cap 1 KiB–1 MiB (owner: 1 MiB is the ceiling), contains ≤ 4 KiB, ≤ 20 JSON assertions with P0-07 paths and value rules, headers `[{"name","value"}]` ≤ 32 with token names, no control characters, no duplicates, no `Authorization`/`Proxy-Authorization`/`Host`, UA, TLS days 1–365 ≤ 10 deduplicated and stored descending, proxy http/https/socks5 without credentials, IP family ""/ipv4/ipv6). Parent exists, profile exists and the dependency cycle walk (bounded at 64) run inside the write transaction. `internal/monitor`: `ParseJSONPath` (P0-07 grammar, ≤ 256 chars, ≤ 32 segments, UTF-8; fuzzed 1.9 M executions clean), `Header`, `JSONAssertion`, `ValidateJSONAssertion`, header token/value checks and the secret name convention `auth.basic` (`user:password`) / `auth.bearer` / `header.<Name>`; `SetSecret` enforces it and refuses basic and bearer together. Mutation-checked: disabling the cycle comparison turns the cycle test red. No audit events (handler concern, M2-17/18).
