# Information Architecture and UX

## Primary navigation

Desktop: collapsible left sidebar.

Sections:
1. Overview
2. Monitors
3. Incidents
4. Status Pages
5. Notifications
6. Maintenance
7. Settings

Sidebar states:
- expanded: icon + label
- collapsed: icon + accessible tooltip
- mobile: drawer

## Overview

Order:

1. Problem strip — only when actionable problems exist:
   - DOWN
   - FLAPPING
   - notification channel unhealthy
   - TLS expiring
   - DB/disk/system warning
2. summary metrics
3. monitor list/summary
4. recent incidents
5. warnings such as certificates or failing channels

Healthy systems should feel calm. Do not fill the dashboard with decorative widgets.

## Monitor list

Use compact responsive rows, not card grids.

Built (M2-16, `GET /monitors`): rows are ordered by name, case-insensitively, and refresh in place from the event stream. The target is `scheme://host` only, shown to admins; viewers see no address. Uptime reads "—" until rollups exist, and the sparkline arrives with charts. The dependency indicator names the parent monitor. With no monitors, admins get a "Create monitor" action (`/monitors/new`) and viewers a plain explanation. Since M2-18 the list refetches itself when a monitor is created or deleted anywhere; only the empty state (which opens no stream) needs a reload.

Each row should provide:
- status icon + text
- name
- type
- public/internal target summary as appropriate
- latest latency
- uptime
- small sparkline/availability hint
- tags
- optional dependency indicator

Actions should be accessible but not visually dominant.

## Monitor detail

Stable route: `/monitors/{id}`

Top section:
- status
- time in current state
- current/last latency
- last check time
- raw and adjusted uptime
- primary latency graph
- availability timeline

Tabs:
1. Overview
2. History
3. Incidents
4. Configuration
5. Diagnostics

Built (M2-18, skeleton): the live header (status, time in state, last latency, last check, uptime "—"), then for admins Edit, Pause or Resume, and Delete. Tabs are links (`?tab=history` …), so each has an address and works without JavaScript. Overview lists the check interval, last success and failure, the certificate expiry when known and the creation date; graphs and uptime arrive with history (M3). History and Incidents are honest empty states until M3. Configuration shows the timing to everyone and, to admins, the request, assertions and advanced settings with secrets by name only. Diagnostics lists the 20 newest failed checks: time, kind, status and duration for everyone; the error message and the response excerpt (as escaped text) for admins only, since both can name internal hosts. Delete asks on a page of its own (no dialog) and says that pausing keeps everything. When the monitor is deleted elsewhere, an open detail page moves to the list.

## Monitor creation/editing

Dedicated page with progressive disclosure.

Sections:
1. Basics
2. Request/target
3. Assertions
4. Retry & timing
5. Dependencies
6. Notifications
7. Advanced

A normal HTTP monitor should require only:
- name
- URL
- interval if overriding default

Built (M2-17, `/monitors/new` and `/monitors/{id}/edit`, admins only): one page, no JavaScript. Basics (type, only HTTP until M4; name; tags; "start checking now" on create), Request (URL, method, POST body, redirects, plain headers as `Name: value` lines, authentication, secret headers), Assertions (status, contains / does not contain, JSON rows: path, check, expected value), Retry & timing (in seconds), Dependencies (parent), Notifications (a placeholder until M5), and Advanced in a collapsed `<details>` that opens when one of its fields has an error. Everything but name and URL has a default. A failed save lists every problem at the top, linked to its field, and marks each field. Secrets are write-only (`13_AUTH_SECURITY.md`). Pausing is not on the form: a new monitor can start paused, an existing one is paused from its page.

## Incidents

Group events by incident.

An incident timeline may include:
- first failure
- confirmation retry
- declared down
- notification sent/failed
- manual note
- recovery
- recovery notification

Do not build PagerDuty-style incident workflow states.

## Settings

Sections:
- General
- Appearance
- Authentication
- Notifications
- Data & retention
- Backup
- System

## Command palette

`Ctrl/Cmd + K`

Supports:
- navigation
- open monitor
- create monitor
- pause/resume monitor
- go to incidents
- switch theme

## Keyboard shortcuts

Suggested:
- `/` focus search
- `G` then `D` overview
- `G` then `M` monitors
- `G` then `I` incidents
- `N` new monitor
- `Ctrl/Cmd + K` palette

Do not create a full Vim-like interface.

## Mobile

Optimize for:
- current status
- checking incidents
- graphs
- pause/resume
- notification/channel health
- maintenance awareness

Complex configuration remains functional but is secondary.
