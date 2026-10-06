# Notifications

## Channels

V1:
- SMTP email
- Telegram
- Discord
- generic webhook

### Channel configuration

Each channel stores one typed configuration, encrypted as a whole in `notification_channels.config_enc` (AAD: table, column and the channel id, so a copied value does not open on another channel). The type is fixed once the channel exists.

| Type | Fields (secret ones marked) |
|---|---|
| SMTP | server, port, security (STARTTLS or TLS), user name, **password**, from, recipients (up to 20) |
| Telegram | **bot token**, chat id (numeric, or `@channelname`) |
| Discord | **webhook URL** (it carries a token) |
| Webhook | URL, optional extra header name, **header value** |

Secrets are write-only in the UI: an input is never filled in, a stored one is announced as "saved", and leaving it empty on edit keeps it. A password belongs to its user name, and a header value to its header name: changing the name means entering the value again. Recipients and the from address are bare addresses, without a display name. URLs may not carry user info. Validation shows every problem at once.

### SMTP delivery

`notify.SendEmail` speaks SMTP with the standard library only:
- security `tls`: implicit TLS from the first byte (usually port 465); `starttls`: a plain connection that must be upgraded. A server that does not offer STARTTLS gets nothing beyond EHLO: there is no fallback to plain text, so a password never travels in clear.
- TLS 1.2 or later, certificate checked against the system roots for the configured host.
- AUTH PLAIN only when a user name is set.
- One delivery is bounded at 30 seconds (or the caller's shorter context), connection to QUIT; the connection is closed when the context ends, so a server that stops answering cannot hold a sender.
- The message is one `text/plain; charset=utf-8` part, quoted-printable; a non-ASCII subject is RFC 2047 encoded. No HTML part (docs/36). The EHLO name is `localhost`, so the instance's host name is not disclosed.
- An error names the failed step and the server's answer on one line (`smtp: authentication failed: 535 5.7.8 …`, `smtp: recipient x@example.com refused: 550 …`, `smtp: connect: timed out`); it never holds the password.

### Telegram delivery

`notify.SendTelegram` calls the Bot API `sendMessage` with `chat_id` and the plain text of the message:
- No `parse_mode` is set, so a name or reason is never read as Markdown or HTML; link previews are off. Text is cut at Telegram's 4096 characters.
- Success is HTTP 200 with `"ok": true`. Anything else is an error that names the status and Telegram's own `description` on one line (`telegram: 400 Bad Request: Bad Request: chat not found`).
- The bot token is part of the request URL, so errors never include the URL: a connection failure reads `telegram: timed out` or `telegram: connection refused`.
- One delivery is bounded at 15 seconds (or the caller's shorter context). Redirects are never followed (shared by all HTTP senders, `http.go`), and at most 4 KiB of an answer is read.

### Discord delivery

`notify.SendDiscord` posts the embed of `Message.Discord()` to the channel webhook (title, description, colour by kind, `allowed_mentions.parse` empty so no name or reason can ping anyone). HTTP 2xx is success.
- **Rate limits are bounded.** On 429 the wait comes from `retry_after` in the body (seconds, may be fractional), else the `Retry-After` header, else one second. A wait of at most 5 seconds is slept once (it ends early with the context) and the post is retried once. A longer wait, or a second 429, returns `*notify.RateLimitError{After}` (`discord: rate limited, retry in 2m 00s`) without further calls; the dispatcher (M5-08) owns the retry schedule. The sender never loops.
- Other failures read `discord: 404 Not Found: Unknown Webhook` (status plus Discord's `message`, one line, an HTML error page is never quoted). The webhook URL carries a token, so no error includes it.
- One delivery is bounded at 15 seconds per call; redirects are not followed.

### Webhook delivery

`notify.SendWebhook` POSTs the stable JSON payload of docs/36 (`Message.Webhook()`) with `Content-Type: application/json`, `User-Agent: Sinjal` and, when configured, the one extra header (name and value as stored; the value is the secret).
- Any 2xx is success. Anything else is a failure, a 3xx included: redirects are never followed, so neither the payload nor the configured header can be forwarded to a host the owner did not choose.
- An error is `webhook: <status> <text>` and nothing more. The endpoint's answer is never quoted (it can be an internal diagnostic page or echo a header), and neither the URL (it may carry a token) nor the header value appears anywhere. Connection failures give the reason only (`webhook: connection refused`, `webhook: timed out`, a certificate error), without the address.
- One delivery is bounded at 15 seconds (or the caller's shorter context), TLS 1.2+ with the system roots for `https`, plain `http` allowed because the URL is the owner's choice. Retries and the schedule belong to the dispatcher (M5-08).

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
