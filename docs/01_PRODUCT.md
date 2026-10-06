# Product Scope

## Problem

Existing uptime monitors often become larger platforms with broad integrations, multiple services, heavier frontends, or accumulated features a single owner does not need.

Sinjal is for a user who wants to know:
- is my service up?
- is it responding quickly?
- did it go down?
- for how long?
- was I notified?
- is the certificate expiring?
- did a scheduled job call home?
- what happened historically?

The answer should not require operating an observability stack.

## Primary user

A technically capable self-hosting developer/operator running personal and small-business services.

## Product principles

1. **Reliable before broad.**
2. **Fast by construction.**
3. **Single-node first.**
4. **Excellent visual design is compatible with low resource use.**
5. **Configuration should be obvious.**
6. **Monitoring data must survive restarts.**
7. **Alerts should be meaningful, not noisy.**
8. **No enterprise theater.**
9. **No speculative extensibility.**
10. **Recovery and migration should be simple.**

## V1 capabilities

### Monitoring
- HTTP/HTTPS
- TCP connect
- ICMP ping
- DNS query
- heartbeat
- status code assertions
- body contains / does-not-contain
- JSON path/value assertion
- custom headers
- basic/bearer/custom authorization stored as encrypted secret material
- redirects configurable
- TLS expiry warnings
- retries
- timeout
- dependency suppression
- maintenance windows

### History
- current latency
- min/avg/max
- p95 where resolution permits; aggregated ranges may show approximate p95
- uptime
- availability timeline
- incident history
- long-term rollups

### Alerting
- SMTP
- Telegram
- Discord
- webhook
- reusable profiles
- severity routing
- quiet hours
- one unresolved reminder
- recovery notice
- delivery-health tracking

### Status pages
- multiple
- public/private/password/unlisted
- branding
- custom hostname
- groups
- manual notes
- feeds/API

### Administration
- password/passkey/TOTP
- viewer accounts
- backup/restore
- system health
- theme choice
- compact/comfortable density
- command palette
- keyboard shortcuts

## Explicit non-goals

See `docs/25_NON_GOALS.md`.

## Definition of v1 quality

V1 is not a "thin MVP." It is a polished narrow tool.

A capability that ships in v1 should feel complete:
- clear empty states
- validation
- error messages
- tests
- documentation
- mobile behavior
- theme coverage
- accessibility
- migration compatibility
