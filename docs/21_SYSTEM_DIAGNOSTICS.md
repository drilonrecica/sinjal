# System Diagnostics

## Purpose

Sinjal should expose enough internal information to prove it remains lightweight and healthy, without turning into its own observability platform.

## Settings -> System

Show:
- version
- uptime
- current memory estimate
- goroutine count
- DB size
- filesystem free space
- raw result count
- active monitors
- checks/minute
- worker active/limit
- result queue depth
- notification queue depth
- last backup
- last retention run
- last migration
- channel health summary

Not built yet (M9). Sources that already exist:
- worker active/limit and queue depth: `scheduler.Pool.Stats()` (`07_SCHEDULER.md` "Worker pool")
- result queue depth and write warning: `results.Processor.Stats()` (`09_DATABASE.md` "Busy handling")
- last retention run: `system_settings.last_retention_run`, written by the daily job (`09_DATABASE.md` "Retention jobs")
- last migration: `schema_migrations.applied_at`
- channel health: `notification_channels.health_state`

## Endpoints

`/healthz`
- app process responsive
- DB basic query succeeds

`/readyz`
- startup complete
- migrations complete
- app ready to serve

Do not expose sensitive details publicly.

Built (`internal/web/health.go`):
- both answer GET and HEAD without authentication, with a plain-text body (`ok` / `unhealthy`, `ready` / `not ready`) and `Cache-Control: no-store`; nothing internal is included
- `/healthz` runs `SELECT 1` on the reader pool with a 2 s timeout, so a long write transaction cannot make it fail; `200` or `503`
- `/readyz` is `503` until `serve` marks startup, including migrations, complete; then `200`

## Logs

Default:
- human-readable stdout/stderr

Optional:
- structured JSON

Environment: `SINJAL_LOG_FORMAT=text|json`

Do not store application logs in SQLite.

Logging conventions are in `30_LOGGING_ERROR_HANDLING.md`.

## Metrics export

No Prometheus exporter in v1.
