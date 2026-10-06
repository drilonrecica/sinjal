# Implementation Checklist

Claude Code should update this file as milestones are completed.

## Milestone 0
- [ ] repository foundation
- [ ] config
- [ ] SQLite
- [ ] migration runner
- [ ] health endpoints
- [ ] base UI
- [ ] theme scaffolding

## Milestone 1
- [ ] password auth
- [ ] sessions
- [ ] CSRF
- [ ] encryption key
- [ ] secret envelope
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
- M0-13: `web/static` embedded via `web.Static`; `internal/assets` serves content-hashed URLs (immutable cache, ETag/304, gzip precompressed at startup, explicit mime table); htmx 2.0.11 (0BSD) vendored with provenance header; `/static/*` route helper `web.RegisterStatic` (wired into `serve` with the first pages in M0-15).
