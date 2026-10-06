# Monitoring Engine

## General contract

Every active monitor type implements the same conceptual execution contract:

Input:
- monitor identity
- type-specific configuration
- timeout/deadline
- execution context

Output:
- started timestamp
- finished timestamp
- duration
- success/failure
- failure kind
- human-readable failure message
- limited diagnostic metadata
- optional latency
- optional protocol-specific fields

Do not expose this as an unnecessary public interface hierarchy unless concrete test substitution requires it.

## Common behavior

Default:
- interval: 30s
- timeout: explicit per monitor
- failure threshold: 2
- retry delay after first failure: 5s
- recovery threshold: 1
- jitter: small bounded scheduler jitter for recurring checks

Paused monitors:
- are not scheduled
- preserve history and incidents
- do not count pause time as outage (pause intervals are excluded from uptime; see `10_INCIDENTS.md`)
- pausing closes an active incident without a recovery notification
- UI must clearly distinguish PAUSED from DOWN

## HTTP(S)

### Supported methods
V1:
- GET
- HEAD
- POST

Additional methods may be added later only if there is a concrete need.

### Features
- URL
- follow redirects toggle
- custom headers
- optional request body for POST
- basic/bearer/custom auth via encrypted secret fields
- custom User-Agent
- timeout
- status assertion
- text assertions
- JSON assertion
- TLS expiry checks
- IP family preference optional advanced setting
- proxy optional advanced setting

### Default success status
`200-399`

Allow expressions:
- `200`
- `200-299`
- `200,204`
- `200-399`

Validate strictly.

### Text assertions
Support:
- contains
- does not contain

Text matching should be byte/string based and documented as case-sensitive by default.

### JSON assertion
Support one or more simple assertions:
- JSON path
- operator
- expected value

V1 operators:
- equals
- not equals
- exists
- does not exist

Do not add arbitrary scripting.

#### Path syntax (decision P0-07)

A small, strict subset:

```text
path    = "$" segment*
segment = "." name            ; name = [A-Za-z_][A-Za-z0-9_]*
        | "[" json-string "]" ; any key, JSON string escaping: ["content-type"]
        | "[" index "]"       ; index = 0 | [1-9][0-9]*
```

Examples: `$.status`, `$.data.items[0].state`, `$["x-version"]`, `$[2]`.

Not supported: wildcards, filters, slices, negative indices, recursive descent, functions. Limits: path ≤ 256 characters, ≤ 32 segments.

#### Expected value

Stored as a typed JSON scalar: string, number, boolean or null.

Form input that parses as a JSON number, `true`/`false`, `null` or a quoted JSON string takes that type; any other input is a string. The form shows how the value will be compared ("compared as: number") so the type is never ambiguous.

#### Evaluation

- body parsed with `encoding/json` using `UseNumber`; duplicate keys: last wins
- `equals` is type-aware:
  - numbers compare numerically (`1` equals `1.0`)
  - strings compare byte-for-byte (case-sensitive)
  - booleans and null compare by value
  - different types are never equal (`42` ≠ `"42"`)
  - an object or array never equals a scalar
- `not equals` is the negation of `equals` and requires the path to exist; a missing path fails the assertion
- `exists`: the path resolves (a `null` value exists)
- `does not exist`: the path does not resolve
- all assertions must pass

Failures:
- `json_parse`: body is not valid JSON, or the body was truncated at the read cap
- `json_assertion`: snippet names path, operator, expected and actual value (actual truncated to 200 bytes)

### Body limit
Hard read cap: default **1 MiB**.

The monitor should stop reading once enough data exists to evaluate assertions where practical.

Failure snippets:
- stored only on failed checks
- small cap, e.g. 4 KiB
- sanitize invalid UTF-8
- never persist known secret request values

### Connection reuse
Reuse HTTP transports/clients keyed by connection-relevant configuration.

Do not create a new transport per check.

### TLS
For HTTPS:
- capture certificate expiry
- warning thresholds configurable
- sensible defaults: 30, 14, 7 days
- warnings are not DOWN if the request succeeds
- a warning is an indicator alongside UP, not a monitor state (there is no `DEGRADED` state)
- the last observed certificate `not_after` is stored per monitor and drives the warning badge and the "TLS expiring" filter
- crossing a threshold sends one `warning`-severity notification per threshold per certificate; a renewed certificate (new `not_after`) resets the thresholds
- expired/invalid TLS that prevents the configured request from succeeding is a failure unless insecure TLS is explicitly enabled

"Insecure skip verify" may exist only in Advanced with clear warning.

## TCP

Inputs:
- host
- port
- timeout

Success:
- TCP connection established within timeout

Measure connect duration.

Close socket promptly.

Implementation (`internal/monitor/tcpcheck`): one `net.Dialer.DialContext` under the monitor timeout; nothing is sent or read, and the connection is closed right after the connect time is taken. Failure kinds: `timeout` (no connection within the timeout), `dns` (name did not resolve), `connect` (refused, unreachable and other dial errors), `unknown` (cancelled by shutdown, not a verdict on the target). Validation: host is an IP or DNS name (no scheme, port, brackets or zone), port 1-65535, timeout above zero. Private and loopback targets are allowed.

## ICMP

Inputs:
- host
- timeout

V1:
- one ping probe per scheduled check is acceptable
- report RTT

Implementation (decision P0-04):
- hand-written on `golang.org/x/net/icmp`; no ping library
- one echo request per scheduled check, bounded by the monitor timeout
- IPv4 and IPv6
- socket order:
  1. unprivileged ICMP datagram socket (`udp4` / `udp6`), allowed when the process GID is within `net.ipv4.ping_group_range`
  2. raw socket (`ip4:icmp` / `ip6:ipv6-icmp`) if the process has `CAP_NET_RAW`
  3. otherwise fail with error kind `permission` and a message naming the fix (sysctl or capability, see `16_DEPLOYMENT.md`)
- in unprivileged mode the kernel rewrites the echo identifier, so replies are matched by sequence number plus a random per-probe payload nonce, not by identifier
- RTT measured with monotonic time from send to matching reply
- the socket mode that works is cached per address family; a later permission change is picked up on restart
- package `internal/monitor/icmpcheck`: a `Pinger` opens one socket per check and closes it on every path; a hostname is resolved first and its first address is pinged
- failure kinds: `timeout` (no matching reply), `dns`, `permission` (neither socket allowed; the message names `net.ipv4.ping_group_range` and `CAP_NET_RAW`), `connect` (other socket or send errors, e.g. no IPv6), `unknown` (cancelled by shutdown)

Failure must be clear if the runtime lacks permission. Never require a privileged container.

## DNS

Inputs:
- hostname
- query type
- optional resolver
- optional expected values

V1 query types:
- A
- AAAA
- CNAME
- MX
- TXT
- NS

Success:
- query completes without configured failure condition
- if expected values exist, returned answer set satisfies configured match

Use explicit timeout.

Resolution (amended at M4-04): `internal/monitor/dnscheck` sends one query itself with `golang.org/x/net/dns/dnsmessage`, not through `net.Resolver`. The stdlib resolver reports NXDOMAIN and an empty answer as the same error, answers A/AAAA from `/etc/hosts`, appends search domains and returns the name itself from `LookupCNAME` when there is no CNAME, which would hide exactly what a DNS monitor is meant to see.
- the query is for the fully qualified name (no search list, no hosts file), recursion desired, EDNS0 with a 1232-byte UDP size; a truncated UDP answer is repeated over TCP
- the reply must carry the query's id and question; anything else on the socket is ignored
- resolver: the configured one (`IP` or `IP:port`, port 53 by default), else the `nameserver` lines of `/etc/resolv.conf` tried in order, each with an equal share of the time left (`127.0.0.1:53` when there are none)
- the answer is every record of the query type in the answer section, whatever its owner name (an A query for an alias returns the target's addresses)
- other error rcodes (SERVFAIL, REFUSED, ...) and an unreachable resolver are `dns_error`; no answer within the timeout is `timeout`; a check cancelled by shutdown is `unknown`

Always a failure, even without expected values:
- NXDOMAIN (`dns_nxdomain`)
- empty answer for the query type (`dns_no_answer`)
- timeout / resolver unreachable (`timeout` / `dns_error`)

### Expected values (decision P0-13)

Per-monitor match mode:
- `all` (default): every expected value must appear in the answer; extra answers are allowed. Detects a changed or removed record.
- `any`: at least one expected value must appear. Suits CDNs and round-robin records.

Normalization before comparing (applied to both expected and returned values):
- names (CNAME, NS, MX host): lowercase, trailing dot stripped
- A/AAAA: parsed as `netip.Addr` and compared canonically (`2001:DB8::1` equals `2001:db8:0::1`); A accepts IPv4 only, AAAA IPv6 only
- MX: expected value is `host` (matches any preference) or `pref host` (both must match)
- TXT: the character-strings of one record are joined with no separator (as for SPF), then compared exactly and case-sensitively
- CNAME: compared against the CNAME target(s) in the answer; no further query follows the chain

Failure kind `dns_mismatch`; the snippet lists missing expected values and the returned answers (capped).

## Heartbeat

Purpose:
monitor scheduled jobs or external processes that call Sinjal.

Configuration:
- generated token
- expected interval
- grace period
- optional human-readable source label

Endpoint:
- authenticated by secret token in path or bearer form
- accept GET or POST for convenience
- POST preferred in docs

State:
- UP after a valid beat
- DOWN when `last_beat + expected_interval + grace` is exceeded
- heartbeat token stored hashed where possible; only reveal once on creation/regeneration

Do not store arbitrary heartbeat payloads in v1.

Implementation (M4-05):
- token: 32 random bytes, base64url without padding (43 characters); only its SHA-256 is stored (`heartbeat_monitor_config.token_hash`). `store.SetHeartbeatToken` issues a new one and the old one stops working at once; the admin routes that reveal it once (create, regenerate with re-authentication) come with the form (M4-06)
- endpoint: `GET|POST /api/v1/heartbeat/{token}`, or `POST /api/v1/heartbeat` with `Authorization: Bearer <token>`; `204` when recorded, `404` for an unknown token, `429` (with `Retry-After`) over 60 requests a minute from one client address, failed guesses included. The body is never read; the access log records the route pattern, never the token
- a beat stores `last_beat_at`, hands a successful result to the result processor (so state, incidents and notifications follow the usual rules) and moves the monitor's deadline job a period ahead. A paused monitor's beat is recorded and changes nothing else
- deadline: a period (`expected interval + grace`) after the last beat, or after the monitor was created or last resumed when that is later, so a beat from before a pause does not make a resumed monitor late at once
- the deadline is a scheduler job (`07_SCHEDULER.md` "Heartbeat monitors"); when it finds no beat in time it stores a failure of kind `heartbeat_missed` ("no heartbeat since <time> (expected every 1m0s, grace 30s)"). The failure threshold and retry delay apply as for any check, so with the defaults (2, 5 s) the monitor is DOWN at the deadline plus 5 s
- restart-safe: the deadline is computed from stored times, so a monitor that missed its deadline while Sinjal was stopped fails at once after the start

## TLS-only behavior

No separate TLS monitor type in v1. TLS expiry is part of HTTPS monitoring.

## Response/body privacy

Do not store successful response bodies.

For failures, only store capped diagnostics required to explain assertion errors.
