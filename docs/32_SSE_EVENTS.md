# SSE Contract

## Purpose

SSE keeps dashboards current without polling or WebSocket complexity.

## Connection

Authenticated stream: `GET /events`.

Public status pages may use either:

- their own visibility-safe SSE stream later, if needed, or
- a lightweight timed refresh or HTMX polling.

Built (M7-08): a plain 60 s meta refresh, so public pages carry no script at all.

Do not expose admin events to public pages.

## Event names

V1 internal/admin events:

```text
monitor.updated
monitor.created
monitor.deleted
incident.opened
incident.updated
incident.closed
notification.channel_updated
maintenance.updated
system.warning
```

All except `system.warning` are sent today; see [Events sent today](#events-sent-today).

## Payload principles

Payloads should contain only enough information for the browser to refresh the affected component.

Prefer:

```json
{
  "monitor_id": "..."
}
```

and trigger an HTMX fragment refresh rather than duplicating the entire application state over SSE.

For high-frequency latency updates, a small presentation-safe payload is acceptable if it avoids needless round trips.

## IDs/reconnect

Use SSE event IDs when useful.

Browser reconnect should:

- be safe
- not replay secret data
- not cause duplicate state changes

Sinjal state is authoritative on the server. Missing an SSE event must not corrupt client behavior, because fragments can be refreshed.

## Backpressure

Each connected browser must have bounded buffering.

If a slow client falls behind:

- drop/close that stream
- let the browser reconnect

Never allow one browser to block the monitor result processor.

## Keepalive

Send lightweight keepalive comments periodically if required by proxies.

## Shutdown

Graceful server shutdown closes streams cleanly.

## Implementation

`internal/web/sse` (`Hub`) is mounted by `web.RegisterEvents` as `GET /events` behind `RequireAuth`. Admins and viewers get the stream; anyone else is answered like any other signed-out page request.

### Events sent today

| Event | Sent when | Payload | Sender |
|---|---|---|---|
| `monitor.updated` | a check result has been stored (after the commit, once per monitor per batch); a monitor was paused or resumed | `monitor_id` | `internal/engine`, result processor |
| `monitor.updated` | a monitor was edited | `monitor_id` | the monitor handlers (M2-17) |
| `monitor.created` | a monitor was created | `monitor_id` | the monitor handlers (M2-17) |
| `monitor.deleted` | a monitor was deleted | `monitor_id` | the monitor handlers (M2-18) |
| `maintenance.updated` | a maintenance window was created, edited or deleted | `maintenance_id` | the maintenance handlers (M3-07) |
| `incident.opened` | a committed batch opened an incident | `incident_id`, `monitor_id` | result processor, through `engine.New`'s `incidents` function |
| `incident.updated` | a timeline entry was added to a running incident (a notification held back or released), or an admin added a note | `incident_id`, `monitor_id` | result processor; the incident handlers (M3-11) |
| `incident.updated` | a notification was sent or given up (`notification_sent`, `notification_failed`), or held by quiet hours | `incident_id`, `monitor_id` | the dispatcher (M5-08) |
| `incident.closed` | a recovery committed, or pausing the monitor ended its incident | `incident_id`, `monitor_id` | result processor; `Engine.Pause` |
| `notification.channel_updated` | a delivery attempt was recorded for a channel: its health, last success or last failure changed | `channel_id` | the dispatcher (M5-08); a test send (M5-11) |

Incident events:

- All three carry both ids, so a monitor's Incidents tab and an incident's own page can tell whether they are affected. The browser refetches the list or timeline whole and picks nothing out of the payload.
- They are sent after the commit, from the processor's goroutine, and never block it.
- A failed batch announces nothing; its retry announces once.
- Deleting a monitor deletes its incidents with it and sends only `monitor.deleted`.

### Browser side

Since M8-05 the app shell opens the only stream. The `Live` component (`web/templates/monitor_row.templ`) wraps the shell's columns in one `<div class="live" hx-ext="sse" sse-connect="/events" hx-trigger="…">`.

- `hx-trigger` names every event the pages use: `monitor.*`, `incident.*`, `maintenance.updated`, `notification.channel_updated`. The extension only listens for the events named there.
- The stream stays open while the pane changes, since moving between pages swaps only the pane.
- The vendored htmx SSE extension (`web/static/js/htmx-ext-sse.js`) owns the `EventSource` and its reconnects. It re-dispatches each named event as a DOM event on that div.

`web/static/js/live.js` (first-party, ~90 lines, renders nothing) does the rest:

- `sse:monitor.updated`: read `monitor_id`, then ask every element marked `data-live` with that `data-monitor-id` to `refresh`. Such an element (the column's `ColumnRow`, `MonitorHeader`, the detail Overview tab's body `MonitorOverview`) carries `hx-get` of its own fragment (`/fragments/monitors/{id}/row|header|overview`), `hx-trigger="refresh"` and `hx-swap="outerHTML"`, so htmx fetches the fragment and replaces the element.
- `sse:monitor.created`: refresh every `data-live-list` element. The list column (`ColumnList`, `hx-get="/fragments/monitors"`) is one, so a new monitor appears in name order, rendered by the server.
- `sse:monitor.deleted`: an element showing that monitor with `data-gone-href` (the detail header) sends the browser there (`/monitors`). Every `data-live-list` is refreshed, which drops the row.
- `sse:maintenance.updated`: refresh every `data-live-list`, among them the Maintenance page's list (`MaintenanceSections`, `hx-get="/fragments/maintenance"`). A window starting or ending is not an event: the sections move on the next refresh or reload.
- `sse:notification.channel_updated`: refresh every `data-live-list`, among them the Notifications page's channel list.
- `sse:incident.opened|updated|closed`: refresh every `data-live-incidents` element (incident lists and timelines).
- `htmx:sseOpen` (first load, reconnect, server restart): refresh every `data-live-incidents` and `data-live-list` element, and every `data-live` element that is not inside a list (a list brings its rows along), since nothing is replayed after a gap.

Other scripts:

- `web/static/js/toast.js` announces the stream itself, never each event (rows update silently). It shows a polite "Live updates are paused. Reconnecting…" when the stream has been lost (`htmx:sseError`/`sseClose`) for 8 s, and "Live updates resumed." when it opens again. A shorter gap says nothing (docs/22 "Live updates").
- `web/static/js/beats.js` listens to the same swaps (`htmx:beforeSwap`, `htmx:load`) only to slide a beat bar that gained a beat and to keep its keyboard focus. It does not change what is fetched.

Why a script instead of an `hx-trigger` filter such as `sse:monitor.updated[...]`: htmx runs trigger filters through `eval`, which the page turns off (`allowEval: false`, no `unsafe-eval` in the CSP). The payload is never rendered. An id that matches nothing is ignored, and a malformed payload is dropped.

### Frame

```text
id: 42
event: monitor.updated
data: {"monitor_id":"…"}
```

- The payload is one line of JSON holding only the ids listed in the table above.
- `id` is a counter of all events since the process started. Nothing is replayed for a `Last-Event-ID`, and after a restart the counter starts again. A browser therefore refreshes what it shows whenever its stream opens (first load, reconnect, restart); a gap in the ids is only a hint that something was missed.
- A stream starts with a comment and `retry: 3000`, flushed at once, so the browser sees the connection open and waits 3 s before reconnecting.

### Bounds

- 256 events may wait per client. `Publish` never blocks: a client whose buffer is full has its stream closed (logged at WARN, counted in `Hub.Stats().Dropped`) and reconnects. The result processor is never held up by a browser.
- Events that pile up while a write is in progress go out together in the next write.
- Each write has its own 10 s deadline, set through `http.ResponseController`. The server's `WriteTimeout` (30 s) covers a whole response and would otherwise end every stream. A client that has stopped reading costs one bounded write; then its stream ends.
- A comment (`: keepalive`) every 20 s keeps proxies with the usual 60 s idle timeout from closing a quiet stream.
- Responses carry `X-Accel-Buffering: no` and `Cache-Control: no-store`.

### Session

`RequireAuth` only checks when the stream opens, so the stream checks again at every keepalive whether its session still exists. Within 20 s of a logout, a session expiring or a viewer being disabled, the stream ends. A database error during that check is logged and does not end the stream.

### Stream shutdown

`serve` registers `Hub.Close` with `http.Server.RegisterOnShutdown`. Every stream is ended after what was already queued for it has been written, and new streams are refused with 503. A graceful shutdown therefore does not wait out its grace period on open streams.

Reverse proxies must not buffer the response (`16_DEPLOYMENT.md` "Reverse proxy").
