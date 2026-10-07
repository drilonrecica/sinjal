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

Implementation (`results.Processor`, SQL in `internal/store/incidents.go`), in the transaction that stores the check result and changes the state:

- the incident is written when the state becomes DOWN. `started_at` is the first failure of the run of failures that reached the threshold (with a threshold of 1, the failing check itself); `created_at` is the check that met the threshold
- `initial_failure_kind` is the kind of that first failure; `summary` is the message of the failure that confirmed the outage
- two events: `detected` at the first failure with its message, `declared_down` at the confirming check with its message
- the first failure is remembered in memory with the failure count. After a restart while PENDING the count starts again (`09_DATABASE.md`), so an outage confirmed after it starts at the first failure after the restart
- a monitor that stays DOWN writes nothing. Should a monitor become DOWN while it already has an active incident (rows edited by hand), that incident is kept and nothing is added: the partial unique index allows one active incident per monitor, and a batch that cannot be written would hold up every monitor

## Recovery

Default recovery threshold: one success.

On recovery:
- close active incident
- calculate duration
- persist recovery event
- notify according to profile

Implementation: when the state goes from DOWN to UP, in the same transaction, `ended_at` is set to the time of the check that met the success threshold and a `recovered` event is added whose message is the duration (`down for 4m17s`, from the stored times, whole seconds). The duration itself is not stored: it is `ended_at − started_at`.

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

Implementation (`incident.FlapStarts`, `incident.FlapEnded`, `store.RecentTransitions`, applied by the result processor in the batch transaction):

- nothing about flapping lives in memory. When an incident opens or a check closes one, the processor reads the monitor's last 4 incidents and counts their transitions; that read is the window, before and after a restart alike
- a transition's time is what the incident stores: an opening counts at `started_at` (the first failure, not the confirming check), a recovery at `ended_at`. Times are compared in whole seconds, as stored
- an incident ended by a pause has no recovery: its end is not a transition (its start is)
- a transition counts while it is less than 10 minutes old, so four transitions need to fit into 9 min 59 s; the overlay ends once the last transition is 10 minutes old
- entry: the transition that is the fourth sets `flapping_since` to its check's time and decides one `flapping` intent; its own `down` or `recovery` intent is already suppressed
- exit is settled before the state machine sees the result, for the state the monitor was in: `flapping_since` is cleared and one intent is decided, `down` for the active incident if the monitor is DOWN, `stable` otherwise. The result is then an ordinary one: if it is itself a transition, its intent is delivered and it is the first of a new window. A monitor that was DOWN through the end of flapping and recovers with that result therefore gets `down` followed by `recovery`
- for a flapping monitor the last transition is read once per batch; other monitors cost nothing extra per result
- an overlay with no transition behind it (a row edited by hand) ends at the next result

## Maintenance

During maintenance:
- checks continue unless explicitly paused
- incidents may still be recorded
- notifications can be suppressed
- adjusted uptime may exclude the window

The UI must show maintenance overlays.

Evaluation (`internal/maintenance`, pure; decision in the result processor):

- a window is in effect from each occurrence's start for its duration (elapsed time)
- `none`: one occurrence at `starts_at`. `daily`: every day from the day of `starts_at`, at its local time of day in the instance time zone (`SINJAL_TIMEZONE`). `weekly`: the same, on the local days whose weekday is in the mask; the start's own day counts only if it is in the mask. There is nothing before `starts_at`
- daylight saving: the local time of day is kept, so a 02:00 window stays at 02:00 local time and its UTC time moves. A time of day that does not exist on a day (the hour skipped in spring) moves forward by the gap (02:30 → 03:30); one that exists twice (the hour repeated in autumn) is the first of the two, so the window occurs once that day. A window spanning the change lasts its duration in elapsed time
- overlapping occurrences (a window longer than its period) merge
- scope: every monitor, or listed monitors and monitors with listed tags (`08_DATA_MODEL.md`)

What it does:

- notifications: when an intent is decided, a window covering the monitor with `suppress_notifications` in effect at that check suppresses it (reason `maintenance`, first in the order). A suppressed DOWN stays pending and is decided on the first result after the window if the monitor is still DOWN (see "Notification intents", catch-up)
- `incidents.maintenance_overlap`: set when the incident opens if a covering window (suppressing or not) was in effect between its start and its confirmation, and when it closes if one was in effect at any time during it
- checks continue, and incidents are recorded as usual
- the windows covering a monitor are read once per batch, and only when an intent is decided or an incident opens or closes

## Pausing

Pausing a monitor:
- closes its active incident, if any, at the pause time with an incident event `paused`
- sends no recovery notification
- clears the FLAPPING overlay
- records the pause interval (`monitor_pauses`)

Resuming closes the pause interval and sets the state to PENDING until the first check result; from there the normal state machine applies.

Implementation (`store.PauseMonitor`, `store.ResumeMonitor`, called through the engine, `07_SCHEDULER.md` "Pause and resume"):

- pause, in one transaction: `enabled = 0`, `current_state = 'paused'`, `current_state_since` = pause time, `flapping_since` cleared, one open row in `monitor_pauses`
- resume, in one transaction: `enabled = 1`, `current_state = 'pending'`, `current_state_since` = resume time, the open pause row closed. A monitor created disabled has a pause row opened at its creation (since M3-08; monitors created disabled before then have none, and their time before the first resume counts as observed)
- pausing a paused monitor, or resuming one that is not paused, changes nothing: the pause interval keeps its start
- `enabled` and the paused state always change together; startup schedules the enabled monitors
- the pause transaction also ends the active incident at the pause time, with a `paused` event carrying the duration. No `recovered` event is written. After a resume a new outage is a new incident

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

Implementation (`incident.Uptime`, pure; `store.Uptime` reads its inputs):

- inputs are intervals only: `monitors.created_at`, the monitor's `monitor_pauses` and `incidents` overlapping the range (both read through their `(monitor_id, start)` indexes; an open pause or active incident ends at now), and the occurrences of the maintenance windows covering the monitor with `exclude_from_adjusted_uptime`, expanded in the instance time zone. Raw check results are never read, so retention changes nothing and no union with aggregates is needed
- times count in whole seconds, as stored; overlapping intervals count once; the result is two integers (up seconds, observed seconds) per ratio
- display (`Ratio.Percent`): integer hundredths of a percent, truncated (`99.99%`, never `100.00%` with any downtime); no data is `—`
- a monitor created paused gets a pause interval from its creation, closed by its first resume, so the time before it was ever checked is not observed time

## Notification intents

The result processor decides what a state change calls for and whether it may be sent. Sending is the dispatcher's job (`11_NOTIFICATIONS.md`); the decision is made where the state changes, in the same transaction, so the two cannot disagree.

Kinds (`incident.IntentKind`):

| Kind | When |
|---|---|
| `down` | an incident opened; or flapping ended while the monitor is DOWN |
| `recovery` | a check closed an incident (not a pause) |
| `flapping` | the monitor started flapping |
| `stable` | flapping ended while the monitor is not DOWN |
| `tls_warning` | a check saw a certificate reach a warning threshold not yet recorded for it in `tls_warnings` (`06_MONITORING_ENGINE.md` "TLS"); belongs to no incident |
| `reminder` | the monitor is still DOWN when its profile's reminder duration has passed since the incident started; decided once per incident (`11_NOTIFICATIONS.md` "Outage reminder") |

Suppression (`incident.Suppression`, a pure function of the kind and three conditions). One reason is recorded; when several apply the order is maintenance, parent, flapping:

| Condition | Suppresses |
|---|---|
| inside a maintenance window that suppresses notifications | every kind |
| parent monitor is DOWN | every kind except `tls_warning` |
| monitor is flapping | `down`, `recovery` and `reminder` |

What is kept:

- a suppressed intent about an incident adds a `notification_suppressed` event to it, message `<kind>: <reason>` (for example `down: flapping`)
- every intent is logged (`notification intent`, with kind, monitor, incident and reason) and, after the commit, handed to the dispatcher with its reason. Nothing leaves the processor for a batch that was not committed
- a monitor that stays DOWN, a restart, a pause and a monitor edit produce no intent
- the dispatcher (`11_NOTIFICATIONS.md` "Dispatcher") adds what the processor cannot know: `notification_suppressed` with `<kind>: quiet_hours` when the profile's quiet hours hold the intent, and for every channel the outcome, `notification_sent` (`down via Ops chat`) or `notification_failed` (with the last error). `down_notified_at` and `recovery_notified_at` are set when the dispatcher takes a DOWN or RECOVERY on, before its first send; a second intent of that kind for the incident is never sent

The flapping condition comes from `monitors.flapping_since`; the parent condition from the parent's stored state (see "Parent dependency"). The maintenance condition is supplied with maintenance evaluation (M3-06).

A held-back DOWN (catch-up):

- a `down` intent suppressed by the parent or by maintenance leaves the DOWN *pending*: the incident's latest DOWN decision is that suppression
- on every result of a monitor that is still DOWN with a pending DOWN, the conditions are evaluated again; once neither the parent nor maintenance holds, a `down` intent is decided for the active incident and a `notification_resumed` event is added, which ends the pending state
- if the monitor is flapping by then, that `down` is suppressed by flapping and the end of the flapping announces it; a DOWN suppressed by flapping is never pending
- the pending state is read from the incident's events (`store.PendingDown`), once per batch and only for DOWN monitors, so a restart neither loses nor repeats it
- a monitor that recovers while held back gets its recovery decided (suppressed if the condition still holds) and no DOWN afterwards

## Parent dependency

If parent is DOWN:
- child's own failure may still produce incident/history
- child notification is suppressed
- UI indicates dependency suppression

When parent recovers, child notification behavior resumes based on child actual state.

Implementation:

- the parent is the monitor's direct `parent_monitor_id`; it is DOWN when its stored `current_state` is `down` (PENDING, PAUSED and FLAPPING-while-up are not). A grandparent is not consulted: a DOWN grandparent makes the parent DOWN first in practice
- the parent's state is read inside the batch transaction (`store.ParentDown`), so a parent that went down earlier in the same batch counts; it is read only when an intent is decided
- an incident that opens while the parent is DOWN has `suppressed_by_parent = 1`; it is recorded and counts towards uptime like any other
- when the parent recovers, a child that is still DOWN decides its held-back DOWN on its next result, at most one check interval later (see "Notification intents", catch-up). There is no fan-out from the parent's recovery to its children
- the parent's and child's checks are independent: a child confirmed DOWN before its parent is announced normally
- UI: the monitor row and detail header show "Parent down: notifications held" next to "Depends on …" while the parent is DOWN; it refreshes with the child's own live updates

## Restart

Active incident survives restart.

Do not:
- create a duplicate incident
- send duplicate initial DOWN notification solely because the process restarted

Incidents are opened and closed only by a change of the stored state, never by a check result as such. After a restart the monitor is still DOWN in its row, further failures change nothing, and the recovery closes the incident that was open before the restart.

A DOWN notification is sent at most once: `down_notified_at` is claimed before the first send, so neither a restart nor a repeated intent can send it again. A delivery that was still waiting or on the network when the process stopped is not resumed after the restart; it is logged (`19_RELIABILITY.md`).

Upgrade: migration 004 opens an incident for every monitor that is DOWN at that moment, starting at `current_state_since`, so a DOWN monitor always has its active incident.

## Manual notes

Allow:
- title/message attached to incident
- timestamps

Implementation (M3-11): an admin adds a note to an active or ended incident (`POST /incidents/{id}/note`, plain text, 1–1,000 characters). It is the `manual_note` event of the incident with the time it was written; the author is in the audit log (`incident.noted`), not in the timeline. `published` stays 0: notes reach status pages in M7. Since M7-04 the form has a "Show on status pages" checkbox (off by default) that sets `published` when the note is written; it cannot be changed afterwards (docs/12 "Incident history").

Do not implement:
- investigating/identified/monitoring workflow state machine
- assignment
- escalation policy
- incident commander roles
