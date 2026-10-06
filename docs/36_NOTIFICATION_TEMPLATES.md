# Notification Content Specification

## General

Notifications must be concise, operational, and immediately interpretable.

## Rendering rules (M5-03)

`internal/notify/render.go` builds every message from one `Event` value that holds only the safe variables (monitor id, name and type; incident id and start; the time; duration; reason; attempts; latency; certificate expiry; status page URL). There is no template language, so nothing else can reach a message.

- Times are shown in the instance time zone as `2026-10-06 18:42:13 CEST` (date included, so a message read hours later is not ambiguous); a certificate expiry is the date in that zone. Webhook timestamps are RFC 3339 in UTC.
- Durations: `45s`, `4m 17s`, `1h 02m`, `2d 03h` (two units, rounded to the second). Latency: `<1 ms`, `74 ms`, `1.5 s`.
- Severity: DOWN and reminder `critical`; TLS warning and FLAPPING `warning`; RECOVERY and STABLE `info`.
- Names and reasons are made one line (control characters and line breaks become spaces), a reason is cut at 200 characters, so nothing can add an email header or a fake line. Lines whose value is unknown (latency, reason) are left out.
- A TLS warning reads "expires in N days", "expires in 1 day", "expires today" or "has expired". FLAPPING and STABLE name the 10-minute flapping window.
- A simulated message starts with `[TEST]` and its first line is "This is a simulated Sinjal incident."; the webhook payload carries `"test": true`; the Discord embed is grey.
- A status page URL, when there is one, is the last line (`Status: …`) and `status_page_url` in the payload.

## DOWN

Required information:
- monitor display name
- DOWN label
- reason
- timestamp
- attempt count
- latest known latency when available

Example:

```text
API is DOWN
Reason: timeout after 5s
Failed at: 18:42:13
Attempts: 2
Last latency: 74 ms
```

## RECOVERY

Required:
- monitor name
- recovered label
- total incident duration
- current latency when available

Example:

```text
API recovered
Downtime: 4m 17s
Current latency: 51 ms
```

## TLS WARNING

Example:

```text
Website TLS certificate expires in 14 days
Expiry: 2026-10-20
```

## FLAPPING

Example:

```text
API is flapping
Repeated state changes detected in the last 10 minutes.
Further transition notifications are temporarily suppressed.
```

## STABLE

Sent once when flapping ends and the monitor is UP (severity info). If the monitor is DOWN when flapping ends, a normal DOWN notification is sent instead.

Example:

```text
API is stable again
Currently UP. No state changes in the last 10 minutes.
```

## UNRESOLVED REMINDER

Example:

```text
API is still DOWN
Duration: 1h 02m
Reason: connection refused
```

## Simulated alert

Every test/simulated message must be unmistakable:

```text
[TEST] API is DOWN
This is a simulated Sinjal incident.
```

## Channel formatting

### Telegram
Plain text, sent without a parse mode, so nothing in a name or reason is read as formatting.

### Discord
One embed (title, description, colour by kind, timestamp) with `allowed_mentions.parse` empty, so a name or reason such as `@everyone` never pings anyone. Title is cut at 256 characters, description at 4096.

### SMTP
Readable plain text only: the subject is the title, the body the text. An HTML part is not produced; it may be added later as a secondary part.

### Webhook
Stable JSON payload. Events: `monitor.down`, `monitor.recovered`, `monitor.flapping`, `monitor.stable`, `monitor.tls_warning`, `monitor.reminder`. `incident` is `null` when the event has none, `duration_seconds` is set for a recovery and a reminder; optional fields (`timestamp`, `reason`, `attempts`, `latency_ms`, `certificate`, `status_page_url`, `test`) are left out when unknown:

```json
{
  "event": "monitor.down",
  "severity": "critical",
  "monitor": {
    "id": "...",
    "name": "API",
    "type": "http"
  },
  "incident": {
    "id": "...",
    "started_at": "...",
    "duration_seconds": null
  },
  "reason": "timeout after 5s"
}
```

Do not leak configured secrets or internal diagnostic bodies.
