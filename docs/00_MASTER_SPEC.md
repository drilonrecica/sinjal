# Sinjal Master Specification

## Product identity

**Name:** Sinjal  
**Descriptor:** Lightweight self-hosted uptime monitoring.  
**Tagline:** Uptime monitoring without the overhead.

## Purpose

Sinjal exists to provide a personal, self-hosted uptime monitor that is:
- fast
- small
- reliable
- visually excellent
- easy to deploy
- easy to back up
- easy to reason about
- intentionally narrower than large observability platforms

It may be used by others and is open source, but product decisions optimize for a technically capable single owner rather than a SaaS company or enterprise team.

## Deployment model

- one Go binary
- first-class Docker image
- native binary equally supported
- one process
- one `/data` directory
- one SQLite database
- no required external service

## Core technology

- Go
- `chi`
- SQLite via `modernc.org/sqlite`
- templ
- HTMX
- Server-Sent Events
- minimal vanilla JavaScript
- lightweight charting
- embedded static assets

## V1 monitor types

1. HTTP(S)
2. TCP
3. ICMP
4. DNS
5. heartbeat/push monitor

HTTPS monitors additionally support TLS certificate expiry warnings.

## Failure semantics

Default active monitor behavior:

1. Normal check executes.
2. First failure enters pending-failure state.
3. Wait 5 seconds.
4. Retry once.
5. Second consecutive failure opens/continues an incident and sets state DOWN.
6. First later successful check immediately recovers the monitor and closes the active incident.
7. Frequent transitions may mark the monitor FLAPPING and suppress repeated notifications until stable.

Defaults are configurable per monitor, but the UI should present sensible defaults rather than expose unnecessary knobs prominently.

## Monitor states

- UP
- PENDING
- DOWN
- FLAPPING
- PAUSED

There is no `DEGRADED` state. Conditions where the target is reachable but needs attention (v1: TLS certificate expiring soon) are shown as a **warning indicator alongside UP**, not as a monitor state. Warnings never change the monitor state, never open incidents, and never affect uptime.

A failed assertion (status, text, JSON) is an ordinary check failure and follows the normal PENDING → DOWN path.

## Scheduling

- default interval: 30 seconds
- explicit hard timeout per check
- bounded worker pool
- centralized priority-queue scheduler
- jitter to avoid synchronized bursts
- runtime scheduling uses monotonic time where possible
- persisted timestamps are UTC

## Retry/recovery

- default failure threshold: 2
- default confirmation retry delay: 5 seconds
- default success threshold: 1
- recovery notification includes outage duration and current latency

## Dependencies

A monitor may depend on one parent monitor in v1.

When the parent is DOWN:
- child checks may continue
- child incidents may still be recorded
- redundant child notifications are suppressed

No general dependency graph engine is required.

## Maintenance

Support:
- one-time maintenance windows
- simple recurring daily/weekly windows

Maintenance may:
- suppress notifications
- optionally exclude the maintenance interval from adjusted uptime

Show both:
- raw uptime
- adjusted uptime

## History

Retention tiers:

- 0–7 days: raw check results
- 7–30 days: 5-minute aggregates
- 30–365 days: hourly aggregates
- >365 days: daily aggregates

Incident summaries are retained indefinitely.

## Notifications

Built-in:
- SMTP email
- Telegram
- Discord
- generic webhook

Notification profiles are reusable and can route by severity:
- info
- warning
- critical

Support:
- quiet hours per profile
- critical bypass of quiet hours
- one configurable unresolved-outage reminder
- retries on failed notification delivery
- channel health display
- test notification
- simulated incident test flow

## Authentication

Admin:
- password
- optional passkey
- optional TOTP

Sessions:
- secure persistent sessions
- default max lifetime around 30 days
- re-authentication for destructive/security-sensitive actions

Viewers:
- optional read-only accounts
- cannot create, edit, or delete monitors
- cannot access secrets

## Status pages

One instance may host multiple status pages.

Visibility:
- public
- authenticated
- password-protected
- unlisted tokenized URL

Features:
- selected monitors only
- public display names separate from internal names
- groups
- 30-day incident history default, configurable
- logo/title/description/accent
- independently selectable theme
- custom hostname mapping
- manual incident note/message
- JSON status endpoint
- RSS/Atom incident feed
- small, low-contrast "Powered by Sinjal" branding by default, removable

## Themes

Four v1 themes:
- Carbon
- Paper
- Midnight
- Terminal

Themes share layout and semantics but may vary:
- colors
- surfaces
- shadows
- typography emphasis
- corner radius
- chart treatment
- mono usage

Status colors retain consistent semantic meaning across themes.

## UI

Primary nav:
- Overview
- Monitors
- Incidents
- Status Pages
- Notifications
- Maintenance
- Settings

Desktop:
- collapsible left sidebar

Mobile:
- excellent read/response experience
- monitor inspection and pause/resume optimized
- complex editing allowed but not the design priority

## Operational behavior

- daily local backups
- safe config export
- full disaster-recovery backup
- automatic backup before DB migration
- forward-only migrations
- rollback by restoring pre-upgrade backup
- no automatic self-updater
- update awareness may be optional/manual, but no unsolicited outbound update checks
- manual release builds and uploads

## Open-source policy

- MIT
- issues open
- PRs not accepted
- AI-generated PRs explicitly not accepted
- author-driven architecture
