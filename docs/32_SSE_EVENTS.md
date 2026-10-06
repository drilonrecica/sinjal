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
