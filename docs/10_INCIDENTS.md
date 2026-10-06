# Incident Semantics

## State machine

Normal path:

```text
UP
 |
 | check fails
 v
PENDING
 |
 | confirmation retry succeeds
 +----------> UP
 |
 | confirmation retry fails
 v
DOWN
 |
 | first successful check
 v
UP
```

States: UP, PENDING, DOWN, FLAPPING, PAUSED. There is no `DEGRADED` state.

Warnings (v1: TLS certificate expiring) are indicators alongside UP. They do not enter this state machine, do not open incidents, and do not affect uptime.

## Opening incident

An incident opens only when the failure threshold is met.

Store:
- start time based on first qualifying failure time
- reason
- diagnostic summary
- notification state

## Recovery

Default recovery threshold: one success.

On recovery:
- close active incident
- calculate duration
- persist recovery event
- notify according to profile

## Flapping

Detect repeated UP/DOWN transitions over a rolling window.

Implementation may choose exact threshold, but must document and test it. Suggested starting policy:
- 4 or more state transitions in 10 minutes -> FLAPPING

When FLAPPING:
- continue checks
- continue incident/state recording
- suppress repeated transition notification spam
- send one flapping warning if appropriate
- return to normal behavior after a stable period

Avoid hidden adaptive retry algorithms.

## Maintenance

During maintenance:
- checks continue unless explicitly paused
- incidents may still be recorded
- notifications can be suppressed
- adjusted uptime may exclude the window

The UI must show maintenance overlays.

## Parent dependency

If parent is DOWN:
- child's own failure may still produce incident/history
- child notification is suppressed
- UI indicates dependency suppression

When parent recovers, child notification behavior resumes based on child actual state.

## Restart

Active incident survives restart.

Do not:
- create a duplicate incident
- send duplicate initial DOWN notification solely because the process restarted

## Manual notes

Allow:
- title/message attached to incident
- timestamps

Do not implement:
- investigating/identified/monitoring workflow state machine
- assignment
- escalation policy
- incident commander roles
