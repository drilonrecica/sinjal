# Implementation Plan

Do not implement Sinjal in one giant pass.

Each milestone has an exit condition.

## Milestone 0 — Repository foundation

Create:
- Go module
- package layout
- chi server
- templ pipeline
- embedded assets
- SQLite connection
- migration runner
- config loader
- logging
- `/healthz`, `/readyz`
- base HTML shell
- Carbon/Paper/Midnight/Terminal token scaffolding
- tests and local dev commands

Exit:
- app starts from empty `/data`
- DB initializes
- health endpoints pass
- base page renders
- no external services required

## Milestone 1 — Auth and security foundation

Implement:
- initial admin setup
- password auth
- sessions
- CSRF
- secure cookies
- audit events
- master key generation
- secret envelope
- passkey support
- optional TOTP
- viewer role

Exit:
- admin can securely log in/out
- viewer cannot mutate
- secrets encrypt/decrypt
- auth integration tests pass

## Milestone 2 — Monitor CRUD and HTTP engine

Implement:
- monitor model
- tags
- notification-profile placeholder relation
- HTTP config
- progressive creation/edit UI
- scheduler
- worker pool
- HTTP client pooling
- status expressions
- body cap
- text assertions
- JSON assertions
- TLS expiry metadata
- result processor
- raw history

Exit:
- HTTP monitors run continuously
- failure retry semantics correct
- state persists across restart
- monitor list/detail live-update via SSE

## Milestone 3 — Incidents and history

Implement:
- incident open/close
- timeline events
- flapping
- parent dependency suppression
- maintenance windows
- raw/adjusted uptime
- charts
- history queries

Exit:
- all incident/state tests pass
- graphs show real stored history
- maintenance semantics validated

## Milestone 4 — Additional monitor types

Implement:
- TCP
- ICMP
- DNS
- heartbeat

Exit:
- common semantics reused without forcing generic abstraction
- permissions/errors clearly surfaced
- heartbeat expiry tested

## Milestone 5 — Notifications

Implement:
- channels
- encrypted channel config
- profiles
- severity routing
- quiet hours
- retries
- channel health
- tests
- simulated incident

Exit:
- SMTP/Telegram/Discord/webhook tested
- duplicate suppression correct
- recovery alerts correct

## Milestone 6 — Retention and aggregation

Implement:
- 5m
- 1h
- 1d rollups
- deletion after successful rollup
- daily cleanup task
- long-range history API/query

Exit:
- retention tests pass
- large synthetic dataset rolls correctly
- DB growth remains bounded

## Milestone 7 — Status pages

Implement:
- multiple pages
- groups
- public display names
- public/authenticated/password/unlisted
- theme/accent/logo
- custom hostname mapping
- incident history
- manual incident note
- JSON endpoint
- feed

Exit:
- no internal target leakage
- access modes tested
- custom host routing tested

## Milestone 8 — UX polish

Implement:
- overview problem strip
- compact/comfortable density
- command palette
- shortcuts
- mobile optimization
- polished empty/loading/error states
- all four themes fully tuned
- accessibility pass

Exit:
- app no longer looks like scaffold/admin template
- theme and mobile acceptance criteria pass

## Milestone 9 — Backup, restore, diagnostics

Implement:
- safe config export/import
- full backup
- daily backups
- restore
- pre-migration backup
- system diagnostics
- disk warnings
- DB integrity command

Exit:
- destructive restore tested
- upgrade/rollback dry-run documented
- diagnostics show resource use

## Milestone 10 — Hardening and performance

Implement/fix:
- load benchmarks
- SQLite contention tests
- queue bounds
- graceful shutdown
- 1,000 monitor benchmark
- frontend payload measurement
- image/binary size optimization
- security review

Exit:
- performance budgets materially satisfied or documented exception approved

## Milestone 11 — Release readiness

Complete:
- README
- docs
- screenshots
- CONTRIBUTING
- security policy
- manual release script
- checksums
- SBOM
- signing
- amd64/arm64 artifacts
- changelog

Exit:
- clean install from release artifact
- upgrade from prior test version
- restore from backup
- no automatic publishing
