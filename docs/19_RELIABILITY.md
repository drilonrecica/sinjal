# Reliability and Failure Behavior

## SQLite busy

Retry with bounded backoff.

After retries exhausted:
- surface system error
- do not silently pretend result persisted
- keep process alive if safe

## DB corruption

- detect via integrity check when appropriate
- enter degraded/read-only mode if possible
- stop destructive writes
- show critical admin banner
- never auto-restore over existing data

## Low disk

Warn:
- <10% free
- <5% critical

No aggressive secret retention changes automatically.

## Network timeouts

Every external check has:
- explicit context deadline
- bounded DNS/connect/read behavior

Never rely only on OS default timeout behavior.

## Huge HTTP response

Default body read limit: 1 MiB.

Do not stream huge bodies to disk.

## Restart during incident

Persist state and active incident.

Do not duplicate outage start or notification after restart.

## Clock changes

- UTC persistence
- monotonic in-process scheduling
- timezone only for display/schedules

## Monitoring-server internet outage

Support a designated connectivity/root monitor via dependency configuration.

Do not infer "internet down" from mass failure automatically.

## Notification provider outage

Retry:
- immediate
- 30s
- 2m
- 10m

Then mark delivery failed.

Do not send a stale initial outage notification hours later merely because a provider recovered.

## Graceful degradation

System diagnostics should expose:
- DB availability
- scheduler running
- worker queue depth
- notification failures
- disk warning
- backup failure
- last retention run

## No hidden self-healing that destroys evidence

Never:
- auto-reset DB
- auto-delete history beyond policy
- auto-regenerate encryption keys
- auto-disable monitors because they fail often
