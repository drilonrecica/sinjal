# Information Architecture and UX

## Layout ("Clear", owner decision 2026-10-14)

Master–detail, like Uptime Kuma, with one job per region:

- **Top bar** (slim): the wordmark, then Status Pages, Incidents, Notifications, Maintenance, Settings, and the account menu.
- **Monitor list column** (left, up to ~360 px, scrolls on its own): "Add monitor", search, filter chips (status, type, tags), then one row per monitor: status icon, name, uptime %, a compact beat bar. Keyboard-navigable; the selected monitor is marked by an accent edge as well as colour.
- **Detail pane** (right): Home when no monitor is selected, otherwise the monitor. Incidents, status pages, notifications, maintenance and settings open in the same pane.

Wide screens use the width for more of the same content, never for widgets:
- from 1600 px the pane splits into two columns (monitor: status hero, beat bar, the four numbers and the chart on the left; incidents, recent events and facts on the right; Home the same way)
- from 2200 px a third column, **Live activity**, streams check results and state changes across all monitors
- beat bars and the chart show more history as they get wider

Phones: the list and the detail are separate views; the top bar becomes a menu.

### Built (M8-05, M8-08)

Files: `web/templates/shell.templ`, `css/shell.css`, `js/shell.js`. Components: `33_FRONTEND_COMPONENTS.md` "Shared layout".

**Top bar.** The wordmark links to Home (`/`). Then Status Pages (admins only, since its pages are admin-only), Incidents, Notifications, Maintenance, Settings, a "Commands" button that opens the command palette (shown by JavaScript), and the account menu (a `<details>` labelled with the login, holding Account and Sign out).

**List column.** Loaded once per page load from `/fragments/monitors`.
- Head: "Add monitor" (admins), a search box, the chips All, Down, Up and Paused, and a "More" panel (type, tag, sort, TLS expiring, recently failing, saved views; loaded on first open from `/fragments/column-filters`). They are one form; the server answers with the filtered list. Search, filters and saved views: `29_SAVED_VIEWS_SEARCH.md`.
- Rows: see "Monitor list" below.
- Keyboard: ↑/↓, Home and End move between rows. The row of the monitor in the pane is `aria-current="page"` and has an accent edge.

**Pane navigation.**
- Links and forms are boosted (htmx). The server aims a boosted answer at `#main` (`HX-Retarget`/`HX-Reselect`, `internal/web/boost.go`), so only the pane changes. The column keeps its rows, scroll position and live updates.
- An answer without a pane (sign-in, re-authentication, a public status page) loads as a full page.
- Every shell page links the same CSS and JS, so no page needs assets of its own.
- Back and forward fetch the page again. There is no htmx history cache: a cached page would carry runtime inline styles that the CSP blocks.

**Event stream.** The shell opens the only `/events` stream.

**Wide screens.**
- From 1600 px, Home and a monitor's Overview tab have two columns (see "Overview (Home)" and "Monitor detail").
- From 2200 px, the Live activity column shows the newest checks of all monitors (the engine's in-memory feed, at most 50) merged with the newest 20 incident events, newest first, 40 lines at most. A failed check names its kind and, when there is one, its status code ("Down: HTTP status 503"), as the beat tooltips do. It refreshes on `monitor.updated` and `incident.*` events at most every 3 s, and loads only while it is shown. Event messages are left out, as on Home.

### Phones

Below 48 rem the column and the pane are separate views:
- `/monitors` is the list view: it shows the column. On larger screens the same address shows Home in the pane.
- Every other page shows the pane.
- The top bar stays one row: wordmark, "Menu", Commands, account. "Menu" is a disclosure that starts with Monitors; it opens as a panel under the bar, over the page, and closes on navigation, Escape or a click elsewhere. Without JavaScript it is open from the start and takes its own row in the page.
- The monitor detail's tabs stay one row and scroll sideways; the current tab is scrolled into view, and the right edge fades while more tabs are hidden.

Phone-specific details: "Mobile" below.

## Overview (Home)

Home opens with one calm status sentence ("All 23 monitors are up." / "Postgres primary is down. 3 more monitors are affected."). Then, in order:

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

### Built (M3 follow-up, M8-03, M8-09)

`GET /` (`internal/web/overview.go`, `OverviewBody` in `web/templates/overview.templ`).

**Status sentence.** The hero of the page, with the Ambient light behind it: red when a monitor is down, amber when one flaps, the accent when calm; none in Terminal.
- Down: the first down monitor whose parent is not down is named as the cause. Every other monitor that is down or flapping counts as affected ("Postgres primary is down. 3 more monitors are affected.").
- Flapping: "API is flapping." / "2 monitors are flapping."
- Calm: "All 23 monitors are up.", "21 monitors are up; 2 are paused." ("All" only when every monitor is up). Also forms for waiting for a first check, all paused, and a single monitor by name.

**Problem strip** ("Needs attention"). Present only when there is something to list. Each line is a glyph, text and a link. In order:
1. a system warning: check results are not being saved (the database's words for admins only)
2. down monitors, with for how long
3. flapping monitors
4. enabled notification channels that are failing or failed recently (linking to Notifications, which viewers may open too)
5. certificates of enabled monitors that expire within 14 days or have expired

At most 8 lines, then "and n more on Monitors". The border is red only with an outage or the system warning; otherwise it uses the warning colour.

**Maintenance notes.** The windows in effect and those beginning within 24 hours, with their scope (see "Mobile").

**Figures.** The monitors by state (total, up, down, flapping, pending, paused) as the figures row of the monitor pages, with a link to all monitors. The monitor list itself is the list column: a thousand rows do not belong on the first page.

**Recent incidents.** The active ones and the last 5 ended (the incident list component), with a link to Incidents.

**Recent events.** The last 10 incident timeline entries across monitors: monitor, what happened, when, linking to the monitor and the incident. The entry's message stays on the incident page, as it can name internal hosts.

**Layout.** From 1600 px two columns: sentence, strip, maintenance notes, figures and recent incidents on the left; recent events on the right. Narrower, they follow in that order. (Until the M8-15 sign-off the incidents sat on the right, which left the left half empty on wide screens.)

**Empty state.** With no monitors the page offers to create the first one (admins) or says an admin has not added any (viewers).

**Refresh.** The page body refreshes when an incident changes, a monitor is created or deleted, maintenance changes or a channel's health moves, and once a minute (which also shows a monitor's first result).

**Not yet built.** Disk and database warnings join the strip with M9. Until then the separate "warnings" section is the strip's certificate and channel lines.

## Monitor list

Use compact responsive rows, not card grids.

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

### Built (M2-16, reshaped by M8-05 and M8-16)

The list is the shell's list column (`ColumnList`/`ColumnRow`, `web/templates/monitors.templ`); `/monitors` is the phones' list view (see "Phones").

- **Order:** by name, case-insensitively, unless the list is sorted by "Slowest" (`29_SAVED_VIEWS_SEARCH.md`).
- **Row:** one link to the monitor: state glyph (the state word for assistive technology), name, raw 24-hour uptime ("—" without data), and a 36-beat bar. A long name wraps to two lines on phones.
- **Uptime:** the same raw figure as the detail header. `store.UptimesRaw` reads it for all monitors at once.
- **Beat bar** (M8-16): the last 36 checks, newest on the right, from the engine's memory (`internal/recent`), not SQLite. Reading the last 30 results of every monitor from SQLite costs 48 ms at 1,000 monitors, the list's whole budget (`18_PERFORMANCE.md`). The beats carry no tooltips in the HTML; the first hover fetches them (`/fragments/monitors/{id}/beats`).
- **Live:** each row refreshes in place from the event stream. The whole column refetches when a monitor is created or deleted.
- **Empty:** "No monitors yet." with "Create the first one" (`/monitors/new`) for admins; "No monitors match." when filters hide every monitor.
- **Target:** not shown in the row. For admins it is matched by search: `scheme://host` (HTTP), `host:port` (TCP, IPv6 in brackets), the host (ping), `hostname TYPE` (DNS), a heartbeat monitor's source label or "push" without one. A heartbeat token never appears in a list.
- **Not in the row:** type, target, latest latency, tags and the dependency indicator. The detail header shows type, target (admins), latency and the dependency.

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

### Built (M2-18, M3-10, M3-12, M4-06, M8-05, M8-16)

`MonitorDetail` (`web/templates/monitor_detail.templ`), fed by `internal/web/monitor_detail.go`.

**Header** (`MonitorHeader`, live): name and status, type, target (admins), dependency, beat bar, then time in state, latency, last check and uptime. Then, for admins, Edit, Pause or Resume, and Delete.
- Uptime is the last 24 hours, raw, with "(x adjusted)" next to it when maintenance changes the figure; "—" without data.
- Beat bar (M8-16, replacing the M3-10 sparkline): up to 120 checks from memory, refreshed with the header. It is a tab stop: ←/→, Home and End move a tooltip through the beats.

**Tabs** are links (`?tab=history` …), so each has an address and works without JavaScript.

**Overview.** From 1600 px two columns.
- Left: the last 90 days as day bars with the adjusted uptime (the status page's strip rules, from incident intervals, `12_STATUS_PAGES.md` "Implementation (M7-04)"); then the last 24 hours as on the History tab (summary, figures, latency chart, availability timeline), without the range selector, and a link to the other ranges.
- Right: facts (check interval, last success and failure, certificate expiry when known, creation date), the monitor's active incidents and its last 5 ended ones, and its 8 latest incident events.

**History.** For the range in `?range=` or `?from=&to=` (default 24 hours):
- Range selector: the presets 1 hour, 24 hours, 7 days, 30 days, 90 days and 1 year as links (the shown one is `aria-current`), and a custom range as a plain GET form (`from`, `to`, `datetime-local` in the instance time zone, prefilled with the shown range, no JavaScript). An invalid range is explained and the default shown.
- One or two sentences with every figure: latency, checks and failures, outages and their total, maintenance windows, raw and adjusted uptime. Then the figures as a row.
- Latency chart (uPlot): average and maximum per interval; outage, maintenance and paused bands; a tick at each outage's start; hover values; times in the instance time zone.
- Availability timeline (SVG, no script): up, down, maintenance, paused, no data; each segment titled with its state and times.
- Keys name every colour in text. With no checks in the range there is an empty note and no chart.
- Since M6-05, ranges reaching past raw retention combine raw results with the rollups. Their p95 then reads "p95 (approximate)" in the figures and the summary (`09_DATABASE.md` "History queries").

**Incidents.** The incident list of this monitor (see "Incidents").

**Configuration.** The timing for everyone. For admins also the request, assertions and advanced settings, with secrets by name only. The notification profile by name.

**Diagnostics.** The 20 newest failed checks: time, kind, status and duration for everyone. The error message and the response excerpt (as escaped text) for admins only, since both can name internal hosts.

**Per type** (M4-06):
- Heartbeat: Overview shows "Expects a beat every" (with the grace period), the last beat and when it will be late, instead of a check interval.
- Configuration shows the type by name, then for admins: the target of a TCP or ping monitor; the query of a DNS monitor (host name, record type, resolver, expected values, match); a heartbeat monitor's source label with a "Regenerate push URL" button (re-authentication required; see "Monitor creation/editing").
- Diagnostics names the failure kinds Permission, No such name, No such record, Unexpected answer, DNS error and Missed heartbeat.

**Delete** asks on a page of its own (no dialog) and says that pausing keeps everything. When the monitor is deleted elsewhere, an open detail page moves to `/monitors`.

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

### Built (M2-17, M4-06, M5-11)

`/monitors/new` and `/monitors/{id}/edit`, admins only. One page, no JavaScript.

**Sections:**
- Basics: type; name; tags; "start checking now" (create only)
- Request: URL, method, POST body, redirects, plain headers as `Name: value` lines, authentication, secret headers
- Assertions: status; contains / does not contain; JSON rows (path, check, expected value)
- Retry & timing, in seconds
- Dependencies: the parent
- Notifications: the profile (None, or one of the profiles; with none, a link to create one)
- Advanced: a collapsed `<details>` that opens when one of its fields has an error

**Rules:**
- Everything but name and URL has a default.
- A failed save lists every problem at the top, each linked to its field, and marks each field.
- Secrets are write-only (`13_AUTH_SECURITY.md`).
- Pausing is not on the form: a new monitor can start paused; an existing one is paused from its page.

**Per type** (M4-06):
- On the create page the type is a row of links (HTTP(S), TCP port, Ping, DNS, Heartbeat → `/monitors/new?type=…`, the current one `aria-current`), so it works without JavaScript. Choosing another type starts the form over, which is why it comes first.
- Once created, a monitor's type is fixed; the edit page shows it as text.
- Only the chosen type's section is on the page: Target (host; port for TCP), Query (host name, record type, optional resolver, expected values one per line, match all/any) or Heartbeat (expected interval and grace in seconds, source label). Assertions and Advanced belong to HTTP.
- A heartbeat monitor has no interval or timeout of its own: its cadence is the expected interval.

**Push URL.** Creating a heartbeat monitor answers with a one-time page holding its push URL instead of the redirect: path form and bearer form, `curl` examples, and a copy button from `js/copy.js` that stays hidden without JavaScript. The token is not shown anywhere again. "Regenerate push URL" on the Configuration tab (`POST /monitors/{id}/heartbeat/token`, recent re-authentication) issues a new one on the same kind of page; the old one stops working at once.

## Maintenance

List, grouped by when a window applies:
- in effect now (until when), upcoming (next start, soonest first), past (ended, latest first)
- each row: name, schedule ("Mon, Wed at 22:00 for 1 h"), scope, whether notifications are held, whether it is excluded from adjusted uptime
- times in the instance time zone, named at the top
- empty state that explains what maintenance does
- updates live when a window changes (`maintenance.updated`)

Create/edit (admin):
- one page, no dialog
- start as a local date and time; duration in hours and minutes
- once, daily, or weekly with weekdays
- the two effects
- all monitors, or chosen monitors and tags
- help text says that adjusted uptime is computed from the windows as they are now
- delete is on the edit page and asks first

## Status Pages

### Built (M7-02, M7-03, M7-04)

`/status-pages`, admins only: the pages name hostnames, internal monitor names and the way into password and unlisted pages.

**List.** Each page's title, who may see it, its address (`/status/{slug}`; "Secret address, shown once" for an unlisted page), the number of monitors and its mapped hostnames, with Edit. The empty state explains what a status page is.

**Create and edit.** One page without JavaScript:
- Page: title, address (slug), description
- Access: the four modes with what each means; the page password
- Appearance: theme; accent as `#rrggbb`, checked for contrast against the theme's background and surface; "Powered by Sinjal"; incident history days
- Groups: one name per line, in order. New groups become choices for a monitor after the first save.
- Monitors: one row per monitor of the instance: show, **public name** (required, never prefilled with the monitor's own name), group, position, show latency
- Hostnames: one per line, normalized to lowercase. The instance's own base URL host and a host mapped to another page are refused.

**Secrets.**
- The page password is write-only: empty keeps the stored one; leaving password mode drops it.
- Choosing unlisted issues a secret address on save, shown **once** on a page of its own. The same happens after "Issue a new address", which needs recent re-authentication and stops the old address at once. The edit page never shows it.

**Logo** (M7-03). A section of its own on the edit page: a separate form that uploads at once (PNG or JPEG, at most 512 KB and 1024 × 1024), with the current logo, Replace and Remove. A refused file is explained next to the field; unsaved changes in the main form are not kept.

**Delete** asks on a page of its own.

**Public page** (M7-04, `/status/{slug}`): described in `12_STATUS_PAGES.md` "Rendering". Access enforcement and hostname routing: M7-05 and M7-06 (`12_STATUS_PAGES.md`).

## Notifications

### Built (M5-11)

`/notifications`; viewers read, admins change.

- **Channels:** name, type, health in words (Healthy, Warning, Failed, Disabled, Not used yet), last success, and last failure with its error. The list refreshes on `notification.channel_updated` (`/fragments/notifications`). The empty state explains what a channel is.
- **Profiles:** name, how many monitors use it, its routes in words ("Critical: Mail, Ops chat"; "Routes nothing" when empty), quiet hours ("Quiet 23:00–07:00 Europe/Belgrade, critical bypasses") and reminder. The empty state explains what a profile is; with no channel it says to add one first.
- **Channel edit page:** "Send test notification" sends a `[TEST]` DOWN for an example monitor through the saved configuration (also for a disabled channel). It then shows "sent" or the sender's one-line error on the edit page it redirects to (a reload sends nothing). It is recorded like a delivery (`event_type` `test`), so it moves the channel's health.
- **Profile page** (create and edit, one page):
  - name
  - the routing matrix: channels down, severities across. Each column names what it carries: info (recovery and stable), warning (certificate expiry and flapping), critical (down and still down). A disabled channel is marked.
  - quiet hours: on/off, from and until as times in the instance time zone, critical bypass (on by default)
  - the outage reminder in minutes (empty: none)
  - every problem is listed at once
  - on the edit page, "Simulate incident" and delete (it asks first and says how many monitors lose their profile)
- **Simulate incident:** sends a `[TEST]` DOWN along the profile's critical routes and a `[TEST]` RECOVERY along its info routes to the enabled channels, in parallel. It shows each outcome and what the quiet hours would do with a real incident now. It records nothing but its audit entry: no incident, no delivery row, no health change.
- **Monitor form:** see "Monitor creation/editing". The detail page's Configuration tab names the profile.

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

### Built (M3-11)

- **`/incidents`** lists the active incidents first (newest first), then the latest 100 ended ones. A row shows:
  - the status (text and glyph)
  - the monitor
  - the summary (the message of the confirming failure)
  - the start, in the instance time zone
  - the duration ("so far" while active)
  - the markers "Parent down: notification held", "During maintenance" and "Flapping: notifications held" (a notification of the incident was suppressed because the monitor flapped)

  Viewers see the same. Empty state: "No incidents". It refreshes on `incident.*` events.
- **`/incidents/{id}`** is the timeline in the order it was written: First failure, Declared down, Notification held back / Held notification released (with the reason), Notification sent / Notification failed (the kind, the channel and the last error), Recovered or Monitor paused, and Notes. Admins get a form to add a note (up to 1,000 characters, plain text, shown escaped). The page refreshes when the incident changes.
- The monitor detail's **Incidents tab** is the same list for that monitor, without the monitor's name on each row.

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

Built (M8-06): `Palette` in the shell, also opened by the top bar's "Commands" button. Groups: the monitors that match the typed text (Open; Pause or Resume for admins), Go to (Home, Monitors, the top bar's sections, Your appearance), Create (Add monitor, admins) and Theme (the three themes, saved to the user's appearance). Markup and keyboard model: `33_FRONTEND_COMPONENTS.md` "Shared layout", `22_ACCESSIBILITY.md` "Keyboard".

## Keyboard shortcuts

Suggested:
- `/` focus search
- `G` then `D` overview
- `G` then `M` monitors
- `G` then `I` incidents
- `N` new monitor
- `Ctrl/Cmd + K` palette

Do not create a full Vim-like interface.

Built (M8-07, `js/shortcuts.js`): all six. `/` focuses the list column's search; `g` then `d`, `m` or `i` (within 1.5 s) goes to Home, Monitors or Incidents; `n` opens Add monitor (admins only). When shortcuts are inactive: `22_ACCESSIBILITY.md` "Keyboard".

## Mobile

Optimize for:
- current status
- checking incidents
- graphs
- pause/resume
- notification/channel health
- maintenance awareness

Complex configuration remains functional but is secondary.

### Built (M8-09)

The phone views are the shell's (see "Phones"):
- **Rows** read in the order they show: state, name, uptime ("99.50% uptime, last 24 hours" to assistive technology), beats. A long name wraps to two lines instead of losing its end.
- **Detail, incidents, graphs:** the pane at full width. The Ambient light stays inside the narrower padding, so no page scrolls sideways.
- **Pause/resume:** on the monitor page and in the command palette.
- **Channel health:** in Home's problem strip.
- **Maintenance awareness:** Home and every tab of a monitor page list the windows in effect ("until …") and those beginning within 24 hours ("from …"), linking to Maintenance. Home shows each window's scope; a monitor page shows only the windows covering it. Viewers see them too.
- **Touch targets:** with a coarse pointer, controls are 44 px (`--control-h`), and so are chips, tabs, range links, the wordmark, the account menu's links and links that stand on their own line. Links inside a line of text get padding instead.
