# Notifications

How Sinjal delivers alerts: channels, profiles, routing, quiet hours, retries and channel health. What a message says, and how it looks on each channel, is in `36_NOTIFICATION_TEMPLATES.md`. When an alert is decided is in `10_INCIDENTS.md` "Notification intents".

## Channels

V1:
- SMTP email
- Telegram
- Discord
- generic webhook

### Channel configuration

Each channel stores one typed configuration (`internal/notify/config.go`), encrypted as a whole in `notification_channels.config_enc`. The AAD is the table, the column and the channel id, so a copied value does not open on another channel (`13_AUTH_SECURITY.md` "Secret envelope"). The type is fixed once the channel exists.

| Type | Fields (secret ones marked) |
|---|---|
| SMTP | server, port, security (STARTTLS or TLS), user name, **password**, from, recipients (up to 20) |
| Telegram | **bot token**, chat id (numeric, or `@channelname`) |
| Discord | **webhook URL** (`https` only; it carries a token) |
| Webhook | URL (`http` or `https`), optional extra header name, **header value** |

Form rules:
- Secrets are write-only. An input is never filled in, a stored one is announced as "saved", and leaving it empty on edit keeps it.
- A password belongs to its user name, and a header value to its header name: changing the name means entering the value again.
- Recipients and the from address are bare addresses, without a display name.
- URLs may not carry user info.
- Validation shows every problem at once.

### SMTP delivery

`notify.SendEmail` speaks SMTP with the standard library only.

- Security `tls` is implicit TLS from the first byte (usually port 465). `starttls` is a plain connection that must be upgraded. A server that does not offer STARTTLS gets nothing beyond EHLO: there is no fallback to plain text, so a password never travels in clear.
- TLS 1.2 or later; the certificate is checked against the system roots for the configured host.
- AUTH PLAIN only when a user name is set.
- One delivery is bounded at 30 seconds (or the caller's shorter context), connection to QUIT. The connection is closed when the context ends, so a server that stops answering cannot hold a sender.
- The message is one `text/plain; charset=utf-8` part, quoted-printable; a non-ASCII subject is RFC 2047 encoded. There is no HTML part (`36`).
- The EHLO name is `localhost`, so the instance's host name is not disclosed.
- An error names the failed step and the server's answer on one line (`smtp: authentication failed: 535 5.7.8 …`, `smtp: recipient x@example.com refused: 550 …`, `smtp: connect: timed out`). It never holds the password.

### Telegram delivery

`notify.SendTelegram` calls the Bot API `sendMessage` with `chat_id` and the plain text of the message.

- No `parse_mode` is set, so a name or reason is never read as Markdown or HTML. Link previews are off. Text is cut at Telegram's 4096 characters.
- Success is HTTP 200 with `"ok": true`. Anything else is an error that names the status and Telegram's own `description` on one line (`telegram: 400 Bad Request: Bad Request: chat not found`).
- The bot token is part of the request URL, so errors never include the URL: a connection failure reads `telegram: timed out` or `telegram: connection refused`.
- One delivery is bounded at 15 seconds (or the caller's shorter context).

### Discord delivery

`notify.SendDiscord` posts the embed of `Message.Discord()` to the channel webhook (`36` "Discord"). HTTP 2xx is success.

- **Rate limits are bounded.** On 429 the wait comes from `retry_after` in the body (seconds, may be fractional), else the `Retry-After` header, else one second.
  - A wait of at most 5 seconds is slept once (it ends early with the context) and the post is retried once.
  - A longer wait, or a second 429, returns `*notify.RateLimitError{After}` (`discord: rate limited, retry in 2m 00s`) without further calls. The dispatcher owns the retry schedule. The sender never loops.
- Other failures read `discord: 404 Not Found: Unknown Webhook`: the status plus Discord's `message`, on one line. An HTML error page is never quoted.
- The webhook URL carries a token, so no error includes it.
- One delivery is bounded at 15 seconds per call.

### Webhook delivery

`notify.SendWebhook` POSTs the stable JSON payload of `36` "Webhook" (`Message.Webhook()`) with `Content-Type: application/json`, `User-Agent: Sinjal` and, when configured, the one extra header (name and value as stored; the value is the secret).

- Any 2xx is success. Anything else is a failure, a 3xx included.
- An error is `webhook: <status> <text>` and nothing more. The endpoint's answer is never quoted (it can be an internal diagnostic page or echo a header). Neither the URL (it may carry a token) nor the header value appears anywhere.
- Connection failures give the reason only (`webhook: connection refused`, `webhook: timed out`, a certificate error), without the address.
- One delivery is bounded at 15 seconds (or the caller's shorter context). TLS uses the system roots for `https`; plain `http` is allowed because the URL is the owner's choice.
- Retries and their schedule belong to the dispatcher.

### Shared HTTP rules

Telegram, Discord and webhook deliveries share one client (`internal/notify/http.go`):
- redirects are never followed, so neither the message nor a configured header can be forwarded to a host the owner did not choose
- at most 4 KiB of an answer is read
- errors never contain the request URL

### Channel setup guides

Each channel has a "Send test notification" button on its edit page: use it right after saving. A successful test turns the channel's health to Healthy; a failure shows the sender's one-line error. Secrets are write-only, so an edit that leaves them empty keeps the stored values.

**SMTP.**
- Server and port are your provider's submission endpoint.
- Choose `STARTTLS` (usually port 587) or `TLS` (implicit, usually port 465). There is no plain-text option, and a server that does not offer STARTTLS is refused.
- Use the account's user name and password. With Gmail or similar, use an app password, not the login password. Leave the user name empty only for a relay that needs no authentication.
- "From" must be an address the server lets that account send as. Recipients are up to 20 addresses.
- Typical errors: `authentication failed: 535` (wrong password, or an app password is needed), `recipient … refused: 550` (relay denied or a bad address), `connect: timed out` (port blocked by the host's firewall).

**Telegram.**
- In Telegram, talk to `@BotFather`, send `/newbot` and copy the bot token (`123456:ABC…`).
- For a private chat, open the bot and press Start. For a group, add the bot, then send a message in it.
- Find the numeric chat id by opening `https://api.telegram.org/bot<token>/getUpdates` and reading `message.chat.id` (negative for groups).
- For a public channel, add the bot as an administrator and use `@channelname` as the chat id.
- `chat not found` means the bot has never been in that chat, or the id is wrong.
- The host needs outbound HTTPS to `api.telegram.org`.

**Discord.**
- In the channel's settings choose Integrations, Webhooks, New Webhook, and copy the webhook URL. It contains a token: treat it as a secret.
- Messages are posted as one embed and cannot mention anyone.
- `404 Unknown Webhook` means the webhook was deleted or the URL was truncated.

**Webhook.**
- Enter an `http` or `https` URL that accepts a POST with the JSON payload described in `36_NOTIFICATION_TEMPLATES.md`. Any 2xx answer is success.
- To authenticate, give one extra header name and value (for example `Authorization` and `Bearer …`). The value is encrypted and never shown again.
- Redirects are not followed, so use the final URL.

## Dispatcher (M5-08)

`internal/dispatch` turns the intents the result processor decides (`10_INCIDENTS.md` "Notification intents") into deliveries.

- `Dispatcher.Enqueue` is the processor's intent callback. It never waits. It ignores a suppressed intent (the processor recorded it).
- An intent is dropped, with an error in the log, only when its queue (1,024) is full.
- One goroutine (`Run`) owns every delivery and all database work. Only the sends run beside it, at most 4 at once, each bounded by its sender.

Routing, per intent:
1. The monitor's profile. No profile, or the monitor deleted: nothing is sent.
2. The severity of the kind (`notify.SeverityOf`; see [Severity](#severity)).
3. Quiet hours ([below](#quiet-hours)). A held intent about an incident adds `notification_suppressed` with `<kind>: quiet_hours` to its timeline.
4. The enabled channels the profile routes that severity to. None: nothing is sent.
5. A `down` or `recovery` about an incident is *claimed* first: `down_notified_at` or `recovery_notified_at` is set where it is still NULL. When it is not NULL, the intent is skipped and logged. The claim is written before the first send, so one incident gets one DOWN and one RECOVERY, also across a restart (`19_RELIABILITY.md`).
6. The message is rendered once (`36`) and sent unchanged on every attempt. It is built from the incident: start, confirming failure, failed checks as attempts and the last good latency before the outage; for a recovery, the duration and the newest latency.

Delivery, per channel:
- The channel's configuration is read and decrypted for each attempt and not kept. A channel deleted or disabled meanwhile ends the delivery silently. A configuration that cannot be read counts as a failed attempt with a readable error, never the vault's.
- Attempts follow [Delivery retry](#delivery-retry). A `RateLimitError` lengthens the wait to what the channel asked for, at most 10 min. Deliveries waiting for a sender or a retry are capped at 4,096.
- A retry of a DOWN whose incident has ended by then is dropped, not sent (`19`: no stale initial alert). The first attempt always goes out. Other kinds retry to the end of the ladder.
- A send cut off by shutdown is not recorded: it says nothing about the channel. What is still waiting at shutdown is logged (`notifications not delivered before shutdown`) and never delivered later.
- A failed delivery is logged, recorded and shown; it never produces an intent of its own.

Records, per attempt:
- One row in `notification_deliveries` (`sent`, `failed`, `dropped`; `incident_id` NULL for a notice without an incident).
- It is written in one transaction with the channel's health and, when the attempt is the last, the incident's timeline entry: `notification_sent` `<kind> via <channel name>`, or `notification_failed` `<kind> via <channel name>: <error>`. A dropped retry is a `notification_failed` entry that says the incident had ended.
- Errors are the senders' one-line, secret-free ones, cut at 300 characters.
- After a recorded attempt the SSE hub gets `notification.channel_updated` with the channel id and, for a timeline entry, `incident.updated`.

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

Built: severity is fixed by the message kind (`notify.SeverityOf`). DOWN and the unresolved reminder are critical; TLS expiry and FLAPPING are warnings; RECOVERY and STABLE are info.

## Quiet hours

Per profile.

Rules:
- suppress info/warning during configured quiet hours
- critical may bypass quiet hours by default
- support timezone-aware local display/config
- internally store normalized schedule fields

Implementation:
- A profile turns quiet hours on with `quiet_hours_enabled`.
- `quiet_start` and `quiet_end` are `HH:MM` in the instance time zone (`SINJAL_TIMEZONE`): start in, end out. An end not after the start runs across midnight (23:00 to 07:00).
- `notify.InQuietHours` compares the wall clock only, so the window keeps its local times through daylight saving.
- A held notification is suppressed, not postponed: nothing is sent when the window ends.
- `critical_bypass` (default on) lets critical notifications through; off, they are held like the rest.

### Critical bypass

Which messages count as critical is fixed by their kind, not configurable ([Severity](#severity)).

| Profile setting | During quiet hours |
|---|---|
| `critical_bypass` on (default) | DOWN and REMINDER are delivered as usual; warnings and info are held |
| `critical_bypass` off | everything is held, DOWN and REMINDER included |

- A held notification is dropped, not postponed. A recovery that falls in the window is therefore not sent, even when its DOWN went out through the bypass. The incident page still shows what was sent and what was held.
- The bypass only concerns quiet hours. Routing (which channels get which severity), maintenance windows, parent dependencies and flapping suppression apply to critical messages as to the rest.
- The "Simulate incident" flow ignores quiet hours and says on its result page what they would do with a real incident right now.

## Outage reminder

One optional reminder per active incident after a configured duration.

Do not repeat forever.

Implementation:
- The duration is the profile's `reminder_after_seconds` (no reminder when NULL). It is read from the profile as it is at that moment, so a change applies to running incidents.
- The result processor decides the reminder, like every other intent: on the first result of a monitor that is still DOWN at or after the incident's start plus that duration. So it comes at most one check interval late (a heartbeat monitor's job repeats every period while it is DOWN).
- `incidents.reminder_sent_at` is set in the same transaction. The reminder is decided once per incident, also across a restart.
- A reminder that the maintenance window, the parent or flapping suppresses (recorded as `reminder: <reason>`) is not sent later. An incident that ends first gets none.
- The message (`36` "UNRESOLVED REMINDER") gives the duration so far and the confirming failure's reason. Severity is critical, so the quiet-hours bypass applies to it as to DOWN.

## Delivery retry

Suggested:
- immediate
- +30s
- +2m
- +10m
- then mark failed

Do not queue stale initial alerts indefinitely.

Implementation (see [Dispatcher](#dispatcher-m5-08)): four attempts. The first is immediate; the others come 30 s, 2 min and 10 min after the failure before them (about 12.5 min in all). Then the notification is given up. A DOWN whose incident has ended is dropped at its next retry.

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

The required content of each message kind, with examples, is in `36_NOTIFICATION_TEMPLATES.md`. Exact formatting varies by channel.

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

Built: there is no template language. Messages are built in code from the safe set alone (`36` "Rendering rules").

## Testing

Each channel:
- "Send test notification"

Profile/monitor:
- "Simulate incident" flow that exercises routing without falsifying production history

Clearly mark simulated notifications.

Implementation (M5-11, `03_INFORMATION_ARCHITECTURE.md` "Notifications"):
- "Send test notification" is on a channel's edit page; "Simulate incident" is on a profile's edit page. Both use an example monitor and render with `Test` set (`36` "Simulated alert").
- Each send is bounded at 20 s (or the sender's shorter bound), so the page is written within the server's write timeout. A simulation sends up to 4 at once.
- A test send through a channel is recorded as a final attempt (`event_type` `test`, no incident) and moves the channel's health like any delivery.
- A simulation sends a DOWN along the critical routes and a RECOVERY along the info routes to the enabled channels. It writes nothing but its audit entry, so the history, deliveries and channel health stay true.
- Quiet hours are not applied to a simulation; the result page says what they would do with a real incident now.
