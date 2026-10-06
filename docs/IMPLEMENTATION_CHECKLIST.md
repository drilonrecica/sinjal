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
