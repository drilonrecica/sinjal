# Reliability and Failure Behavior

## SQLite busy

Retry with bounded backoff.

After retries are exhausted:
- surface a system error
- do not silently pretend the result persisted
- keep the process alive if safe

Built: `db.Retry` and the result processor's held batch (`09_DATABASE.md` "Busy handling").

## DB corruption

- detect via integrity check when appropriate
- enter degraded/read-only mode if possible
- stop destructive writes
- show a critical admin banner
- never auto-restore over existing data

Not built yet (M9; `09_DATABASE.md` "DB corruption").

## Low disk

Warn:
- <10% free
- <5% critical

No aggressive secret retention changes automatically.

Not built yet (M9; `09_DATABASE.md` "Disk space").

## Network timeouts

Every external check has:
- an explicit context deadline
- bounded DNS/connect/read behavior

Never rely only on OS default timeout behavior.

Built: every check runs under the monitor's timeout (`06_MONITORING_ENGINE.md`), and shutdown cancels running checks (`07_SCHEDULER.md` "Shutdown").

## Huge HTTP response

Default body read limit: 1 MiB.

Do not stream huge bodies to disk.

Built: see `06_MONITORING_ENGINE.md` "Body limit".

## Restart during incident

Persist state and the active incident.

Do not duplicate the outage start or notification after restart.

Notifications are sent at most once: the incident's `down_notified_at` / `recovery_notified_at` is claimed before the first send.

A delivery that was waiting for a retry, or on the network, when the process stopped is not resumed after the restart. The shutdown logs how many were not delivered. Owner decision (M5-08): no duplicate is ever worth a resumed delivery.

State and incident handling across restart: `07_SCHEDULER.md` "State across restart" and `10_INCIDENTS.md` "Restart".

## Clock changes

- UTC persistence
- monotonic in-process scheduling
- time zone only for display/schedules

## Monitoring-server internet outage

Support a designated connectivity/root monitor via dependency configuration.

Do not infer "internet down" from mass failure automatically.

Built: a parent monitor (`10_INCIDENTS.md` "Parent dependency").

## Notification provider outage

Retry:
- immediate
- 30s
- 2m
- 10m

Then mark the delivery failed.

Do not send a stale initial outage notification hours later merely because a provider recovered.

Built in `internal/dispatch` (`11_NOTIFICATIONS.md` "Dispatcher"):
- a DOWN whose incident has ended is dropped at its next retry
- every other kind runs the ladder to its end, about 12.5 minutes, and is then given up with a `notification_failed` timeline entry and the channel marked `failed`

## Graceful degradation

System diagnostics should expose:
- DB availability
- scheduler running
- worker queue depth
- notification failures
- disk warning
- backup failure
- last retention run

Not built yet (Settings → System, M9; `21_SYSTEM_DIAGNOSTICS.md`). The result processor's write warning is already shown on Home (`09_DATABASE.md` "Busy handling").

## No hidden self-healing that destroys evidence

Never:
- auto-reset DB
- auto-delete history beyond policy
- auto-regenerate encryption keys
- auto-disable monitors because they fail often

Built: `vault.LoadOrCreate` creates `master.key` only when it is missing and the database holds no encrypted values; otherwise startup fails with restore instructions. It never replaces an existing or unreadable key file.
