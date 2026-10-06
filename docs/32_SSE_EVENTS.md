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
| `monitor.created`, `monitor.deleted` | a monitor was created or deleted | the monitor handlers (M2-17, M2-18) |

The other names in the list above arrive with their features.

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
