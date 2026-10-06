# Notifications

## Channels

V1:
- SMTP email
- Telegram
- Discord
- generic webhook

## Profiles

Reusable profile example:

```text
Critical
  info     -> Discord
  warning  -> Telegram + Discord
  critical -> Telegram + Discord + SMTP
```

Profiles are assigned to monitors.

No general expression/rules language.

## Severity

- info
- warning
- critical

Examples:
- recovery: info
- TLS expiring: warning
- monitor down: critical

## Quiet hours

Per profile.

Rules:
- suppress info/warning during configured quiet hours
- critical may bypass quiet hours by default
- support timezone-aware local display/config
- internally store normalized schedule fields

## Outage reminder

One optional reminder per active incident after a configured duration.

Do not repeat forever.

## Delivery retry

Suggested:
- immediate
- +30s
- +2m
- +10m
- then mark failed

Do not queue stale initial alerts indefinitely.

## Channel health

Admin UI shows:
- Healthy
- Warning
- Failed
- last success
- last failure/error

Do not create recursive alerts about failed alerts.

## Notification content

DOWN:

```text
API is DOWN
Reason: timeout after 5s
Failed at: 18:42:13
Attempts: 2
Last latency: 74 ms
```

RECOVERY:

```text
API recovered
Downtime: 4m 17s
Current latency: 51 ms
```

Exact formatting varies by channel.

## Templates

Support a small safe variable set:
- monitor.name
- monitor.type
- incident.started_at
- incident.duration
- error.message
- result.latency
- status
- status_page_url if available

Do not expose arbitrary Go template execution or custom code.

## Testing

Each channel:
- "Send test notification"

Profile/monitor:
- "Simulate incident" flow that exercises routing without falsifying production history

Clearly mark simulated notifications.
