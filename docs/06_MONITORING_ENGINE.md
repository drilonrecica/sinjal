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

## TLS-only behavior

No separate TLS monitor type in v1. TLS expiry is part of HTTPS monitoring.

## Response/body privacy

Do not store successful response bodies.

For failures, only store capped diagnostics required to explain assertion errors.
