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

Stored states: UP, PENDING, DOWN, PAUSED. FLAPPING is an overlay on top of these (see Flapping). There is no `DEGRADED` state.

Warnings (v1: TLS certificate expiring) are indicators alongside UP. They do not enter this state machine, do not open incidents, and do not affect uptime.

### Thresholds

The diagram shows the defaults (failure threshold 2, success threshold 1). In general, with failure threshold F and success threshold S:

- UP or PENDING + success → UP. One success is enough; S only applies to leaving DOWN.
- UP or PENDING + failure → PENDING and one confirmation retry after the retry delay, until F consecutive failures are reached → DOWN. With F = 1 the first failure is DOWN and there is no retry.
- DOWN + failure → DOWN, at the normal interval. No retries while DOWN.
- DOWN + success → UP once S consecutive successes are reached; a failure in between starts the count again.
- PAUSED: results are ignored.

A monitor therefore gets at most F − 1 confirmation retries in a row. A new or resumed monitor is PENDING with nothing counted and follows the same rules as UP.

The decision is one pure function, `incident.Transition` (`internal/incident`).

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

Policy (decision P0-08). Fixed constants in v1, not per-monitor settings.

Transition:
- a confirmed UP → DOWN (incident opened) or DOWN → UP (incident closed)
- PENDING blips that recover on the confirmation retry are not transitions

Enter:
- 4 or more transitions within a rolling 10-minute window
- set `monitors.flapping_since`

Exit:
- 10 minutes with no transition
- clear `flapping_since`
- pausing the monitor also clears it

FLAPPING is an overlay, not a replacement state:
- `current_state` keeps the real health (up/pending/down)
- UI, filters and status pages show FLAPPING while `flapping_since` is set (paused monitors show PAUSED)
- checks continue
- incidents keep opening and closing normally, so history and uptime stay truthful

Notifications:
- on entry: one FLAPPING notification, severity `warning`
- while flapping: DOWN, RECOVERY and reminder notifications are suppressed; each suppression is recorded as an incident event
- on exit: one notification for the current real state: DOWN (critical) if down, otherwise STABLE (info)
- parent-dependency and maintenance suppression still apply on top

Restart:
- `flapping_since` is persisted
- the transition window is rebuilt from `incidents.started_at` / `ended_at`; no separate transition table
- the exit condition is evaluated on the next processed result

Avoid hidden adaptive retry algorithms.

## Maintenance

During maintenance:
- checks continue unless explicitly paused
- incidents may still be recorded
- notifications can be suppressed
- adjusted uptime may exclude the window

The UI must show maintenance overlays.

## Pausing

Pausing a monitor:
- closes its active incident, if any, at the pause time with an incident event `paused`
- sends no recovery notification
- clears the FLAPPING overlay
- records the pause interval (`monitor_pauses`)

Resuming closes the pause interval and sets the state to PENDING until the first check result; from there the normal state machine applies.

## Uptime

Time-weighted, computed from incidents and pause intervals (decision P0-12). It does not use check counts, so single failures that recover on the confirmation retry do not count as downtime, and the same formula works for any range regardless of raw-result retention.

For a monitor and a requested range:

```text
R  = requested range clipped to [monitor.created_at, now)
P  = paused time within R                      (monitor_pauses)
O  = R − P                                     observed time
D  = incident time within O                    (started_at → ended_at, active incident → now)
M  = time within O covered by in-scope maintenance windows
     with exclude_from_adjusted_uptime = 1

raw uptime      = (O − D) / O
adjusted uptime = (O − M − (D − D∩M)) / (O − M)
```

Rules:
- incidents start at the first qualifying failure (see "Opening incident"), so the confirmation period counts as downtime once DOWN is confirmed
- every incident counts, including parent-suppressed and flapping ones; suppression only affects notifications
- denominator 0 (e.g. fully paused range) → "no data", never 100%
- display with two decimals, truncated rather than rounded, so a range with any downtime never shows 100.00%
- maintenance time M is expanded from the current maintenance window definitions; editing or deleting a window that already occurred changes historical adjusted uptime (accepted v1 behavior, documented in the UI help text)
- latency statistics (avg/min/max/p95) come from raw results and aggregates; uptime does not

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
