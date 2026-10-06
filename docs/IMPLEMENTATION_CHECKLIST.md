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
- [ ] password auth
- [ ] sessions
- [ ] CSRF
- [x] encryption key
- [x] secret envelope
- [ ] passkeys
- [ ] TOTP
- [ ] viewer role

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
