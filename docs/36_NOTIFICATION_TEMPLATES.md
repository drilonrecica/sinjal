# Notification Content Specification

## General

Notifications must be concise, operational, and immediately interpretable.

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
Plain text/limited safe formatting.

### Discord
Simple embed acceptable if lightweight.

### SMTP
Readable plain text required.
HTML may be included as secondary part, but do not depend on it.

### Webhook
Stable JSON payload:

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
