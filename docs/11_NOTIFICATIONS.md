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

### Channel setup guides

Each channel has a "Send test notification" button on its edit page: use it right after saving. A successful test turns the channel's health to Healthy; a failure shows the sender's one-line error. Secrets are write-only, so an edit that leaves them empty keeps the stored values.

**SMTP.** Server and port are your provider's submission endpoint. Choose `STARTTLS` (usually port 587) or `TLS` (implicit, usually port 465); there is no plain-text option, and a server that does not offer STARTTLS is refused. Use the account's user name and password (with Gmail or similar, an app password, not the login password). Leave the user name empty only for a relay that needs no authentication. "From" must be an address the server lets that account send as; recipients are up to 20 addresses. Typical errors: `authentication failed: 535` (wrong password or app password needed), `recipient … refused: 550` (relay denied or a bad address), `connect: timed out` (port blocked by the host's firewall).

**Telegram.** In Telegram, talk to `@BotFather`, send `/newbot` and copy the bot token (`123456:ABC…`). For a private chat, open the bot and press Start; for a group, add the bot, then send a message in it. Find the numeric chat id by opening `https://api.telegram.org/bot<token>/getUpdates` and reading `message.chat.id` (negative for groups). For a public channel, add the bot as an administrator and use `@channelname` as the chat id. `chat not found` means the bot has never been in that chat, or the id is wrong. The host needs outbound HTTPS to `api.telegram.org`.

**Discord.** In the channel's settings choose Integrations, Webhooks, New Webhook, and copy the webhook URL (it contains a token: treat it as a secret). Messages are posted as one embed and cannot mention anyone. `404 Unknown Webhook` means the webhook was deleted or the URL was truncated.

**Webhook.** Enter an `http` or `https` URL that accepts a POST with the JSON payload described in `36_NOTIFICATION_TEMPLATES.md`; any 2xx answer is success. To authenticate, give one extra header name and value (for example `Authorization` and `Bearer …`); the value is encrypted and never shown again. Redirects are not followed, so use the final URL.

## Dispatcher (M5-08)

`internal/dispatch` turns the intents the result processor decides (`10_INCIDENTS.md` "Notification intents") into deliveries. `Dispatcher.Enqueue` is the processor's intent callback: it never waits, ignores a suppressed intent (the processor recorded it) and drops an intent, with an error in the log, only when its queue (1,024) is full. One goroutine (`Run`) owns every delivery and all database work; only the sends run beside it, at most 4 at once, each bounded by its sender.

Routing, per intent:
1. The monitor's profile. No profile, or the monitor deleted: nothing is sent.
2. The severity of the kind (`36`: DOWN and reminder critical, TLS warning and FLAPPING warning, RECOVERY and STABLE info).
3. Quiet hours (below). A held intent about an incident adds `notification_suppressed` with `<kind>: quiet_hours` to its timeline.
4. The enabled channels the profile routes that severity to; none: nothing is sent.
5. A `down` or `recovery` about an incident is *claimed* first: `down_notified_at` or `recovery_notified_at` is set where it is still NULL, and when it is not, the intent is skipped and logged. The claim is written before the first send, so one incident gets one DOWN and one RECOVERY, also across a restart (`19_RELIABILITY.md`).
6. The message is rendered once (`36`) from the incident (start, confirming failure, failed checks as attempts, the last good latency before the outage; for a recovery the duration and the newest latency) and sent as it is on every attempt.

Delivery, per channel:
- The channel's configuration is read and decrypted for each attempt and not kept; a channel deleted or disabled meanwhile ends the delivery silently. A configuration that cannot be read counts as a failed attempt with a readable error, never the vault's.
- Attempts: at once, then 30 s, 2 min and 10 min after each failure, then the notification is given up (4 attempts, about 12.5 min). A `RateLimitError` lengthens the wait to what the channel asked for, at most 10 min. Deliveries waiting for a sender or a retry are capped at 4,096.
- A retry of a DOWN whose incident has ended by then is dropped, not sent (`19`: no stale initial alert); the first attempt always goes out. Other kinds retry to the end of the ladder.
- A send cut off by shutdown is not recorded: it says nothing about the channel. What is still waiting at shutdown is logged (`notifications not delivered before shutdown`) and never delivered later.
- Each attempt is one row in `notification_deliveries` (`sent`, `failed`, `dropped`; `incident_id` NULL for a notice without an incident), written in one transaction with the channel's health and, when the attempt is the last, the incident's timeline entry: `notification_sent` `<kind> via <channel name>` or `notification_failed` `<kind> via <channel name>: <error>`. Errors are the senders' one-line, secret-free ones, cut at 300 characters.
- After a recorded attempt the SSE hub gets `notification.channel_updated` with the channel id and, for a timeline entry, `incident.updated`.
- A failed delivery is logged, recorded and shown; it never produces an intent of its own.

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

Implementation: `quiet_start` and `quiet_end` are `HH:MM` in the instance time zone (`SINJAL_TIMEZONE`), start in, end out; an end not after the start runs across midnight (23:00 to 07:00). `notify.InQuietHours` compares the wall clock only, so the window keeps its local times through daylight saving. A held notification is suppressed, not postponed: nothing is sent when the window ends. `critical_bypass` (default on) lets critical notifications through; off, they are held like the rest.

### Critical bypass

Which messages count as critical is fixed by their kind, not configurable: **monitor down** and the **unresolved reminder** are critical; TLS expiry and flapping are warnings; recovery and stable are info (`notify.SeverityOf`).

| Profile setting | During quiet hours |
|---|---|
| `critical_bypass` on (default) | DOWN and REMINDER are delivered as usual; warnings and info are held |
| `critical_bypass` off | everything is held, DOWN and REMINDER included |

- A held notification is dropped, not postponed (see above). A recovery that falls in the window is therefore not sent, even when its DOWN went out through the bypass. The incident page still shows what was sent and what was held.
- The bypass only concerns quiet hours. Routing (which channels get which severity), maintenance windows, parent dependencies and flapping suppression apply to critical messages as to the rest.
- The "Simulate incident" flow ignores quiet hours and says on its result page what they would do with a real incident right now.

## Outage reminder

One optional reminder per active incident after a configured duration.

Do not repeat forever.

Implementation: the profile's `reminder_after_seconds` (none when NULL). The result processor decides it, like every other intent, on the first result of a monitor that is still DOWN at or after the incident's start plus that duration, so it comes at most one check interval late (a heartbeat monitor's job repeats every period while it is DOWN). The duration is read from the profile as it is at that moment, so a change applies to running incidents. `incidents.reminder_sent_at` is set in the same transaction: the reminder is decided once per incident, also across a restart, and a reminder that the maintenance window, the parent or flapping suppresses (recorded as `reminder: <reason>`) is not sent later. An incident that ends first gets none. The message (`36` "UNRESOLVED REMINDER") gives the duration so far and the confirming failure's reason; severity critical, so the quiet-hours bypass applies to it as to DOWN.

## Delivery retry

Suggested:
- immediate
- +30s
- +2m
- +10m
- then mark failed

Do not queue stale initial alerts indefinitely.

Implemented as above (see "Dispatcher"): four attempts, the last three 30 s, 2 min and 10 min after the failure before them; a DOWN whose incident has ended is dropped at its next retry.

## Channel health

Admin UI shows:
- Healthy
- Warning
- Failed
- last success
- last failure/error

Do not create recursive alerts about failed alerts.

Rules (`notification_channels.health_state`, moved by every recorded attempt):

| State | Meaning |
|---|---|
| `unknown` | nothing was ever sent through the channel |
| `healthy` | the latest attempt succeeded |
| `warning` | the latest attempt failed and the notification is still being retried |
| `failed` | a notification was given up after its last attempt; the channel stays `failed` through further failures until an attempt succeeds |

`last_success_at`, `last_failure_at` and `last_error` are kept independently, so a healthy channel still shows its last failure. A dropped retry changes nothing.

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

Implementation (M5-11, `03_INFORMATION_ARCHITECTURE.md` "Notifications"): "Send test notification" on a channel's edit page and "Simulate incident" on a profile's edit page, both for an example monitor, rendered with `Test` set (`[TEST]` title, the simulated first line, `"test": true`, grey embed). Each send is bounded at 20 s (or the sender's shorter bound), so the page is written within the server's write timeout; a simulation sends up to 4 at once. A test send through a channel is recorded as a final attempt (`event_type` `test`, no incident) and moves the channel's health like any delivery. A simulation sends a DOWN along the critical routes and a RECOVERY along the info routes to the enabled channels and writes nothing but its audit entry, so the history, deliveries and channel health stay true; quiet hours are not applied to it, and the result page says what they would do with a real incident now.
