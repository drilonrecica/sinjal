# Data Model

This document defines conceptual entities. Exact SQL is in `spec/schema.sql`.

## User

Fields:
- id
- email/username identifier
- display name optional
- role: admin | viewer
- password hash optional when passkey-only is configured later
- TOTP encrypted secret optional
- last accepted TOTP time step (replay prevention)
- created/updated timestamps
- disabled flag
- UI preferences: theme (NULL = instance default), density (comfortable | compact), sidebar collapsed

Only admin can mutate configuration.

## Session

- id/token hash
- user_id
- created_at
- expires_at
- last_seen_at
- reauthenticated_at
- user agent/IP metadata optional and privacy-limited

Policy in `13_AUTH_SECURITY.md`: 30-day absolute expiry, `last_seen_at` written at most every 5 minutes, re-authentication valid for 10 minutes.

## Passkey credential

- user_id
- credential id
- public key
- sign count
- transports optional
- backup-eligible flag from registration (must not change afterwards)
- label
- created_at
- last_used_at

## Monitor

Shared fields:
- id
- name
- type
- enabled
- current_state
- current_state_since
- interval_seconds
- timeout_ms
- failure_threshold
- retry_delay_ms
- success_threshold
- parent_monitor_id nullable
- notification_profile_id nullable
- created_at
- updated_at
- last_check_at
- last_success_at
- last_failure_at
- flapping_since nullable (FLAPPING overlay; `current_state` stays the real up/pending/down/paused)
- tls_not_after nullable (last observed HTTPS certificate expiry)

## Type-specific monitor config

Separate tables or clear typed storage for:
- HTTP
- TCP
- ICMP
- DNS
- heartbeat

Avoid an unvalidated "anything JSON" model for critical semantics.

Small JSON fields are acceptable for naturally structured values such as non-secret headers or expected DNS answer lists, provided validation is strict.

## Monitor secrets

Encrypted-at-rest values associated with a monitor:
- authorization
- sensitive headers
- proxy credentials
- other credentials

Store separately enough that normal monitor listing queries do not pull secret blobs.

## Monitor pause

Interval log used to exclude paused time from uptime:
- monitor_id
- paused_at
- resumed_at nullable (NULL while paused)

At most one open pause per monitor. Written only on pause/resume.

## Tags

Many-to-many:
- tag
- monitor_tag

Tags are descriptive only. They do not implicitly change notification behavior.

## Check result

Raw recent result:
- monitor_id
- checked_at
- duration_ms
- success
- protocol code/status where relevant
- error kind
- short error message
- small diagnostic snippet
- metadata limited to protocol diagnostics

Retention: 7 days.

## Aggregate bucket

- monitor_id
- bucket_start
- bucket_resolution
- total_count
- success_count
- failure_count
- min_ms
- max_ms
- avg_ms
- p95_ms or documented approximate percentile
- uptime/availability derived or stored

Resolutions:
- 5m
- 1h
- 1d

## Incident

- id
- monitor_id
- started_at
- ended_at nullable
- initial_failure_kind
- summary/cause
- suppressed_by_parent flag
- maintenance_overlap flag
- notification state: down_notified_at, reminder_sent_at, recovery_notified_at (prevents duplicate notifications across restart). `down_notified_at` and `recovery_notified_at` are set by the dispatcher when it takes the notification on, before the first send, so NULL means it was never dispatched: suppressed, unrouted or not yet. `reminder_sent_at` is set by the result processor when the reminder falls due, in the same transaction as the check that decides it, so the reminder is decided once whether it is then delivered, suppressed or unrouted
- created_at

Active incident has `ended_at = NULL`. At most one active incident per monitor (enforced by a partial unique index).

## Incident event/note

Keep a small timeline:
- detected
- declared_down
- notification_sent
- notification_failed
- notification_suppressed (message: intent kind and reason)
- manual_note
- recovered
- paused (pausing the monitor closed the incident)

Each event has a time and an optional message; `recovered` and `paused` carry the outage's duration.

`manual_note` events carry a `published` flag; only published notes appear on status pages.

This is not a workflow engine.

## TLS warning

Per monitor + certificate `not_after` + threshold days:
- notified_at

Deduplicates TLS warning notifications. A renewed certificate (new `not_after`) starts fresh: the rows of a monitor's earlier certificates are deleted when the new one reaches its first threshold. A row means the threshold was decided (delivered, suppressed or unrouted), not that it was delivered.

## DNS config

Includes `match_mode` (`all` | `any`, default `all`) for expected values; semantics in `06_MONITORING_ENGINE.md`.

## Heartbeat config

Includes optional human-readable `source_label`.

## Maintenance window

- id
- name
- starts_at
- ends_at or duration
- recurrence type: none | daily | weekly
- weekday mask for weekly
- suppress_notifications
- exclude_from_adjusted_uptime
- optional monitor/tag scope

Keep recurrence simple.

Stored form (`maintenance_windows`, `internal/store/maintenance.go`):

- `starts_at`: the first occurrence, UTC like every timestamp; a recurring window also takes its time of day from it, read in the instance time zone
- `duration_seconds`: elapsed time of one occurrence, 1 minute to 31 days
- `weekday_mask`: weekly windows only, NULL otherwise; bit *n* is `time.Weekday(n)`, Sunday = bit 0 … Saturday = bit 6
- `scope_json`: NULL for every monitor, otherwise `{"monitors":[ids],"tags":[names]}`; a monitor is in scope if it is listed or carries one of the tags. Tags are kept by name (case-insensitive) because a tag no monitor uses is deleted and would come back with a new id; a monitor or tag that disappears later simply matches nothing

## Notification channel

- id
- name
- type: smtp | telegram | discord | webhook
- enabled
- encrypted configuration blob
- health state
- last success/failure
- created/updated

## Notification profile

- id
- name
- quiet-hours configuration
- unresolved reminder interval optional (`reminder_after_seconds`; NULL: no reminder)
- created/updated

Routes:
- profile_id
- severity
- channel_id

## Notification delivery

- incident/event reference
- channel_id
- attempt
- status
- attempted_at
- error message
- delivered_at

One row per attempt (`notification_deliveries`): `event_type` is the intent's kind (`down`, `recovery`, `flapping`, `stable`, `tls_warning`, `reminder`; `test` for "Send test notification", one final attempt without an incident; a simulated incident writes no row), `attempt` counts from 1, `status` is `sent`, `failed` (this attempt) or `dropped` (not attempted: the incident had ended before the retry), `delivered_at` is set on `sent` only, `incident_id` is NULL for a notice that belongs to no incident.

## Status page

- id
- slug
- title
- description
- visibility
- unlisted token hash optional
- password hash optional
- theme
- accent
- logo path/reference optional
- incident_days default 30
- show_powered_by default true
- created/updated

## Status page groups

Ordered groups.

## Status page monitor mapping

Per mapping:
- monitor id
- public display name
- group
- order
- expose latency yes/no

Never default to exposing internal target hostname/URL.

## Hostname mapping

- hostname
- status_page_id

Reverse proxy handles TLS; app routes based on trusted Host.

## Audit event

Small security/activity log:
- actor
- event type
- object
- timestamp
- minimal metadata

Do not create full field-level audit diff storage.

## Saved view

Per user:
- name (unique per user)
- filters (validated JSON: states, types, tags, text)
- sort
- created/updated

## System setting

Small key/value settings for non-secret instance preferences.

Secrets belong in encrypted storage, not this table.
