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

Built (M3 follow-up, `GET /`): the problem strip ("Needs attention") exists only when there is something to list: monitors that are down (with for how long), flapping ones, and certificates that expire within 14 days or have expired (enabled monitors only; each line is glyph, text and a link to the monitor), at most 8 with "and n more" after them. Then the monitors by state (total, up, down, flapping, pending, paused) as the figures row of the monitor pages, with a link to Monitors, and the latest incidents (the active ones and the last 5 ended, the incident list component) with a link to Incidents. The monitor list itself is on Monitors: a thousand rows do not belong on the first page. Notification channel and DB/disk/system warnings join the strip with their features (M5, M9), and the separate "warnings" section is the strip's certificate lines until then. With no monitors the page offers to create the first one (admins) or says an admin has not (viewers). It refreshes when an incident changes or a monitor is created or deleted, and once a minute, which also shows a monitor's first result.

## Monitor list

Use compact responsive rows, not card grids.

Built (M2-16, `GET /monitors`): rows are ordered by name, case-insensitively, and refresh in place from the event stream. The target is `scheme://host` only, shown to admins; viewers see no address. Uptime reads "—" until rollups exist. The sparkline is on the detail header, not in the rows: reading the last 30 results of every monitor costs 48 ms at 1,000 monitors, the whole budget of the list (`18_PERFORMANCE.md`); rows can get an availability hint from M6 rollups. The dependency indicator names the parent monitor. With no monitors, admins get a "Create monitor" action (`/monitors/new`) and viewers a plain explanation. Since M2-18 the list refetches itself when a monitor is created or deleted anywhere; only the empty state (which opens no stream) needs a reload. Since M4-06 the target column covers every type, still for admins only: `scheme://host` (HTTP), `host:port` (TCP, IPv6 in brackets), the host (ping), `hostname TYPE` (DNS), and a heartbeat monitor's source label, or "push" without one. A heartbeat token never appears in a list.

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

Built (M2-18, skeleton): the live header (status, time in state, last latency, last check, uptime "—"), then for admins Edit, Pause or Resume, and Delete. Tabs are links (`?tab=history` …), so each has an address and works without JavaScript. Overview lists the check interval, last success and failure, the certificate expiry when known and the creation date; graphs and uptime arrive with history (M3). History and Incidents are honest empty states until M3. Configuration shows the timing to everyone and, to admins, the request, assertions and advanced settings with secrets by name only. Diagnostics lists the 20 newest failed checks: time, kind, status and duration for everyone; the error message and the response excerpt (as escaped text) for admins only, since both can name internal hosts. Delete asks on a page of its own (no dialog) and says that pausing keeps everything. When the monitor is deleted elsewhere, an open detail page moves to the list. Per type (M4-06): a heartbeat monitor's Overview shows "Expects a beat every" (with the grace period), the last beat and when it will be late instead of a check interval; Configuration shows the type by name, then for admins the target of a TCP or ping monitor, the query of a DNS monitor (host name, record type, resolver, expected values, match), and a heartbeat monitor's source label with a "Regenerate push URL" button (re-authentication required; see below). Diagnostics names the new failure kinds (Permission, No such name, No such record, Unexpected answer, DNS error, Missed heartbeat).

Charts (M3-10): the header carries a sparkline of the last 30 checks (SVG, failures as ticks, the range in words), refreshed with the header. The History tab shows, for the range in `?range=` or `?from=&to=` (default 24 hours): the range, one or two sentences with every figure (latency, checks and failures, outages and their total, maintenance windows, raw and adjusted uptime), the figures as a row, the latency chart (uPlot: average and maximum per interval, outage, maintenance and paused bands, a tick at each outage's start, hover values, times in the instance time zone) and the availability timeline (SVG, no script; up, down, maintenance, paused, no data, each segment titled with its state and times). Keys name every colour in text. With no checks in the range there is an empty note and no chart.

Range, Overview graph and uptime (M3-12): the History tab starts with the range selector: the presets 1 hour, 24 hours, 7 days, 30 days, 90 days and 1 year as links (the shown one is `aria-current`), and a custom range as a plain GET form (`from`, `to`, `datetime-local` in the instance time zone, prefilled with the shown range, no JavaScript needed; an invalid range is explained and the default shown). The Overview tab shows the last 24 hours below its facts: the same summary, figures, latency chart and availability timeline, no selector, and a link to the other ranges. The detail header's Uptime is the last 24 hours, raw, with "(x adjusted)" next to it when maintenance changes the figure; "—" without data. The monitor list's Uptime column stays "—" until rollups exist: reading it costs about 140 µs per monitor (`BenchmarkUptimeDay`), 140 ms for 1,000 monitors against the list's 50 ms budget, so as with the sparkline it is on the detail page only. Since M6-05 ranges reaching past raw retention combine raw results with the rollups; their p95 then reads "p95 (approximate)" in the figures and the text summary (`09_DATABASE.md` "History queries").

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

Built (M2-17, `/monitors/new` and `/monitors/{id}/edit`, admins only): one page, no JavaScript. Basics (type; name; tags; "start checking now" on create), Request (URL, method, POST body, redirects, plain headers as `Name: value` lines, authentication, secret headers), Assertions (status, contains / does not contain, JSON rows: path, check, expected value), Retry & timing (in seconds), Dependencies (parent), Notifications (the profile, M5-11), and Advanced in a collapsed `<details>` that opens when one of its fields has an error. Everything but name and URL has a default. A failed save lists every problem at the top, linked to its field, and marks each field. Secrets are write-only (`13_AUTH_SECURITY.md`). Pausing is not on the form: a new monitor can start paused, an existing one is paused from its page.

Per type (M4-06): on the create page the type is a row of links (HTTP(S), TCP port, Ping, DNS, Heartbeat → `/monitors/new?type=…`, the current one marked `aria-current`), so it works without JavaScript; choosing another type starts the form over, which is why it comes first. Once created, a monitor's type is fixed and the edit page shows it as text. Only the chosen type's section is on the page: Target (host; port for TCP), Query (host name, record type, optional resolver, expected values one per line, match all/any) or Heartbeat (expected interval and grace in seconds, source label); Assertions and Advanced belong to HTTP. A heartbeat monitor has no interval or timeout of its own: its cadence is the expected interval. Creating a heartbeat monitor answers with a one-time page holding its push URL (path form and bearer form, `curl` examples, a copy button from `js/copy.js` that stays hidden without JavaScript) instead of the redirect; the token is not shown anywhere again. "Regenerate push URL" on the Configuration tab (`POST /monitors/{id}/heartbeat/token`, recent re-authentication) issues a new one on the same kind of page, and the old one stops working at once.

## Maintenance

List, grouped by when a window applies:
- in effect now (until when), upcoming (next start, soonest first), past (ended, latest first)
- each row: name, schedule ("Mon, Wed at 22:00 for 1 h"), scope, whether notifications are held, whether it is excluded from adjusted uptime
- times in the instance time zone, named at the top
- empty state that explains what maintenance does
- updates live when a window changes (`maintenance.updated`)

Create/edit (admin): one page, no dialog; start as a local date and time, duration in hours and minutes, once/daily/weekly with weekdays, the two effects, all monitors or chosen monitors and tags. Help text says that adjusted uptime is computed from the windows as they are now. Delete is on the edit page and asks first.

## Status Pages

Built (M7-02, `/status-pages`, admins only: the pages name hostnames, internal monitor names and the way into password and unlisted pages): the list shows each page's title, who may see it, its address (`/status/{slug}`; "Secret address, shown once" for an unlisted page), the number of monitors and its mapped hostnames, with Edit. Empty state explains what a status page is. Create and edit are one page without JavaScript: Page (title, address/slug, description), Access (the four modes with what each means, the page password), Appearance (theme, accent as #rrggbb checked for contrast against the theme's background and surface, "Powered by Sinjal", incident history days), Groups (one name per line, in order), Monitors (one row per monitor of the instance: show, **public name** — required, never prefilled with the monitor's own name — group, position, show latency) and Hostnames (one per line, normalized to lowercase; the instance's own base URL host and a host mapped to another page are refused). New groups become choices for a monitor after the first save. The page password is write-only (empty keeps the stored one; leaving password mode drops it). Choosing unlisted issues a secret address on save, shown **once** on a page of its own (also after "Issue a new address", which needs recent re-authentication and stops the old address at once); the edit page never shows it. Delete asks on a page of its own. Public rendering, access enforcement and hostname routing arrive with M7-04 to M7-06, and the logo upload with M7-03.

## Notifications

Built (M5-11, `/notifications`; viewers read, admins change):
- Channels: name, type, health in words (Healthy, Warning, Failed, Disabled, Not used yet), last success and last failure with its error. The list refreshes on `notification.channel_updated` (`/fragments/notifications`). Empty state explains what a channel is.
- Profiles: name, how many monitors use it, its routes in words ("Critical: Mail, Ops chat"; "Routes nothing" when empty), quiet hours ("Quiet 23:00–07:00 Europe/Belgrade, critical bypasses") and reminder. Empty state explains what a profile is; with no channel it says to add one first.
- Channel edit page: "Send test notification" sends a `[TEST]` DOWN for an example monitor through the saved configuration (also for a disabled channel), then shows "sent" or the sender's one-line error on the edit page it redirects to (a reload sends nothing), and is recorded like a delivery (`event_type` `test`), so it moves the channel's health.
- Profile page (create/edit, one page): name; the routing matrix, channels down and severities across (each column names what it carries: info recovery and stable, warning certificate expiry and flapping, critical down and still down; a disabled channel is marked); quiet hours (on/off, from and until as times in the instance time zone, critical bypass, on by default); the outage reminder in minutes (empty: none). Every problem is listed at once. On the edit page, "Simulate incident" and delete (it asks first and says how many monitors lose their profile).
- "Simulate incident" sends a `[TEST]` DOWN along the profile's critical routes and a `[TEST]` RECOVERY along its info routes to the enabled channels, in parallel, and shows each outcome and what the quiet hours would do with a real incident now. It records nothing but its audit entry: no incident, no delivery row, no health change.
- Monitor form, Notifications section: the profile (None, or one of the profiles; with none, a link to create one). The detail page's Configuration tab names it.

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
- `/incidents` lists the active incidents first (newest first), then the latest 100 ended ones; a row shows the status (text and glyph), the monitor, the summary (the message of the confirming failure), the start in the instance time zone, the duration ("so far" while active), and the markers "Parent down: notification held", "During maintenance" and "Flapping: notifications held" (a notification of the incident was suppressed because the monitor flapped). Viewers see the same. Empty state: "No incidents". It refreshes on `incident.*` events.
- `/incidents/{id}` is the timeline in the order it was written: First failure, Declared down, Notification held back / Held notification released (with the reason), Notification sent / Notification failed (the kind, the channel and the last error), Recovered or Monitor paused, and Notes. Admins get a form to add a note (up to 1,000 characters, plain text, shown escaped); the page refreshes when the incident changes.
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
