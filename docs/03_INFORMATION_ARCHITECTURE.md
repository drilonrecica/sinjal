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

Built (M2-16, `GET /monitors`): rows are ordered by name, case-insensitively, and refresh in place from the event stream. The target is `scheme://host` only, shown to admins; viewers see no address. Uptime reads "—" until rollups exist. The sparkline is on the detail header, not in the rows: reading the last 30 results of every monitor costs 48 ms at 1,000 monitors, the whole budget of the list (`18_PERFORMANCE.md`); rows can get an availability hint from M6 rollups. The dependency indicator names the parent monitor. With no monitors, admins get a "Create monitor" action (`/monitors/new`) and viewers a plain explanation. Since M2-18 the list refetches itself when a monitor is created or deleted anywhere; only the empty state (which opens no stream) needs a reload.

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

Charts (M3-10): the header carries a sparkline of the last 30 checks (SVG, failures as ticks, the range in words), refreshed with the header. The History tab shows, for the range in `?range=` or `?from=&to=` (default 24 hours; the selector arrives with M3-12): the range, one or two sentences with every figure (latency, checks and failures, outages and their total, maintenance windows, raw and adjusted uptime), the figures as a row, the latency chart (uPlot: average and maximum per interval, outage, maintenance and paused bands, a tick at each outage's start, hover values, times in the instance time zone) and the availability timeline (SVG, no script; up, down, maintenance, paused, no data, each segment titled with its state and times). Keys name every colour in text. With no checks in the range there is an empty note and no chart.

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

## Maintenance

List, grouped by when a window applies:
- in effect now (until when), upcoming (next start, soonest first), past (ended, latest first)
- each row: name, schedule ("Mon, Wed at 22:00 for 1 h"), scope, whether notifications are held, whether it is excluded from adjusted uptime
- times in the instance time zone, named at the top
- empty state that explains what maintenance does
- updates live when a window changes (`maintenance.updated`)

Create/edit (admin): one page, no dialog; start as a local date and time, duration in hours and minutes, once/daily/weekly with weekdays, the two effects, all monitors or chosen monitors and tags. Help text says that adjusted uptime is computed from the windows as they are now. Delete is on the edit page and asks first.

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

Implementation (M3-11):
- `/incidents` lists the active incidents first (newest first), then the latest 100 ended ones; a row shows the status (text and glyph), the monitor, the summary (the message of the confirming failure), the start in the instance time zone, the duration ("so far" while active), and the markers "Parent down: notification held" and "During maintenance". Viewers see the same. Empty state: "No incidents". It refreshes on `incident.*` events.
- `/incidents/{id}` is the timeline in the order it was written: First failure, Declared down, Notification held back / Held notification sent (with the reason), Recovered or Monitor paused, and Notes. Notification sent/failed entries arrive with M5. Admins get a form to add a note (up to 1,000 characters, plain text, shown escaped); the page refreshes when the incident changes.
- The monitor detail's Incidents tab is the same list for that monitor, without the monitor's name on each row.

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
