# System Diagnostics

## Purpose

Sinjal should expose enough internal information to prove it remains lightweight and healthy without turning into its own observability platform.

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

## Endpoints

`/healthz`
- app process responsive
- DB basic query succeeds

`/readyz`
- startup complete
- migrations complete
- app ready to serve

Do not expose sensitive details publicly.

## Logs

Default:
- human-readable stdout/stderr

Optional:
- structured JSON

Environment:
`SINJAL_LOG_FORMAT=text|json`

Do not store application logs in SQLite.

## Metrics export

No Prometheus exporter in v1.
