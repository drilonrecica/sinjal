# SSE Contract

## Purpose

SSE keeps dashboards current without polling or WebSocket complexity.

## Connection

Authenticated stream:
`GET /events`

Public status pages may use either:
- their own visibility-safe SSE stream later if needed, or
- lightweight timed refresh/HTMX polling

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

Sinjal state is authoritative on the server; missing an SSE event must not corrupt client behavior because fragments can be refreshed.

## Backpressure

Each connected browser must have bounded buffering.

If a slow client falls behind:
- drop/close that stream
- let browser reconnect

Never allow one browser to block the monitor result processor.

## Keepalive

Send lightweight keepalive comments periodically if required by proxies.

## Shutdown

Graceful server shutdown closes streams cleanly.

## Implementation

`internal/web/sse` (`Hub`), mounted by `web.RegisterEvents` as `GET /events` behind `RequireAuth`: admins and viewers get the stream, anyone else is answered like any other signed-out page request.

Events sent today:

| Event | Sent when | Sender |
|---|---|---|
| `monitor.updated` | a check result has been stored (after the commit, once per monitor per batch), a monitor was paused or resumed | `internal/engine`, result processor |
| `monitor.updated` | a monitor was edited | the monitor handlers (M2-17) |
| `monitor.created` | a monitor was created | the monitor handlers (M2-17) |
| `monitor.deleted` | a monitor was deleted | the monitor handlers (M2-18) |
| `maintenance.updated` | a maintenance window was created, edited or deleted; payload `{"maintenance_id":"…"}` | the maintenance handlers (M3-07) |

The other names in the list above arrive with their features.

### Browser side

Pages that show live monitors wrap them in the `Live` component (`web/templates/monitor_row.templ`): one `<div hx-ext="sse" sse-connect="/events" hx-trigger="sse:monitor.updated, sse:monitor.created, sse:monitor.deleted">` (the extension only listens for the events named there). The vendored htmx SSE extension (`web/static/js/htmx-ext-sse.js`) owns the `EventSource` and its reconnects and re-dispatches each named event as a DOM event on that div. `web/static/js/live.js` (first-party, ~35 lines, renders nothing) does the rest:

- `sse:monitor.updated`: read `monitor_id`, then ask every element marked `data-live` with that `data-monitor-id` to `refresh`. Such an element (`MonitorRow`, `MonitorHeader`) carries `hx-get` of its own fragment (`/fragments/monitors/{id}/row|header`), `hx-trigger="refresh"` and `hx-swap="outerHTML"`, so htmx fetches the fragment and replaces the element.
- `sse:monitor.created`: refresh every `data-live-list` element. The monitor list (`MonitorRows`) is one, with `hx-get="/fragments/monitors"`, so a new monitor appears in name order, rendered by the server.
- `sse:monitor.deleted`: an element showing that monitor with `data-gone-href` (the detail header) sends the browser there (`/monitors`); every `data-live-list` is refreshed, which drops the row.
- `sse:maintenance.updated`: refresh every `data-live-list`. The Maintenance page's own `<div sse-connect>` names only this event, and its list (`MaintenanceSections`, `hx-get="/fragments/maintenance"`) is the `data-live-list` there; the monitor pages do not listen for it. A window starting or ending is not an event: the sections move on the next refresh or reload.
- `htmx:sseOpen` (first load, reconnect, server restart): refresh every `data-live-list` and every `data-live` element that is not inside one (a list brings its rows along), since nothing is replayed after a gap.

Why a script instead of an `hx-trigger` filter such as `sse:monitor.updated[...]`: htmx runs trigger filters through `eval`, which the page turns off (`allowEval: false`, no `unsafe-eval` in the CSP). The payload is never rendered; an id that matches nothing is ignored, and a malformed one is dropped.

Frame:

```text
id: 42
event: monitor.updated
data: {"monitor_id":"…"}
```

- the payload is one line of JSON with the monitor id and nothing else
- `id` is a counter of all events since the process started. Nothing is replayed for a `Last-Event-ID`, and after a restart the counter starts again. A browser therefore refreshes what it shows whenever its stream opens (first load, reconnect, restart); a gap in the ids is only a hint that something was missed
- a stream starts with a comment and `retry: 3000`, flushed at once, so the browser sees the connection open and waits 3 s before reconnecting

Bounds:

- 256 events may wait per client. `Publish` never blocks: a client whose buffer is full has its stream closed (logged at WARN, counted in `Hub.Stats().Dropped`) and reconnects. The result processor is never held up by a browser
- events that pile up while a write is in progress go out together in the next write
- each write has its own 10 s deadline, set through `http.ResponseController`, because the server's `WriteTimeout` (30 s) covers a whole response and would otherwise end every stream. A client that has stopped reading costs one bounded write; then its stream ends
- a comment (`: keepalive`) every 20 s keeps proxies with the usual 60 s idle timeout from closing a quiet stream. Responses carry `X-Accel-Buffering: no` and `Cache-Control: no-store`

Session: `RequireAuth` only looks when the stream opens, so the stream asks again at every keepalive whether its session still exists. Within 20 s of a logout, a session expiring or a viewer being disabled the stream ends. A database error during that check is logged and does not end the stream.

Shutdown: `serve` registers `Hub.Close` with `http.Server.RegisterOnShutdown`. Every stream is ended, after what was already queued for it has been written, and new ones are refused with 503, so a graceful shutdown does not wait out its grace period on open streams.

Reverse proxies must not buffer the response (`16_DEPLOYMENT.md`).
