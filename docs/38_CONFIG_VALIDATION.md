# Configuration Validation

Rules every configuration write must pass. Bounds that are built are named in code next to the rule (`internal/store/validate.go` for monitors unless noted).

## General

Reject invalid configuration at write/import time, not during monitor execution.

Built: every field is checked on each write, and the form shows all problems at once, each next to its field.

## Monitor common

- name non-empty
- interval bounded to sane minimum
- timeout >0 and less than interval unless explicitly justified
- thresholds >=1
- parent cannot equal self
- dependency cycles rejected
- assigned notification profile must exist

Suggested minimum active interval:
- 10 seconds

Do not allow sub-second or high-frequency monitoring in v1.

Built bounds: interval 10 s to 24 h (`MinIntervalSeconds`, `MaxIntervalSeconds`); failure and success thresholds 1 to 100. A heartbeat monitor has no timeout; its interval is its expected interval.

## HTTP

- valid http/https URL
- method one of GET/HEAD/POST
- status expression parsable
- body cap within safe bounds
- JSON path syntax validated against the subset in `06_MONITORING_ENGINE.md`
- at most 20 JSON assertions per monitor
- `exists` / `does not exist` take no expected value; `equals` / `not equals` require one
- TLS warning days positive and deduplicated
- secret headers separated from normal display where configured

Built bounds: body cap 1 KiB to 1 MiB (the hard read cap of `06`; an admin may only lower it); a request body only for POST, at most 64 KiB. Credentials in the URL, and `Authorization`, `Proxy-Authorization` or `Host` as headers, are refused (`13_AUTH_SECURITY.md` "Monitor secrets in the form").

## TCP

- valid host
- port 1–65535

## ICMP

- valid hostname/IP

## DNS

- valid hostname
- supported record type
- resolver valid when provided (an IP address, optionally with a port, `IP` or `IP:port`; default port 53)
- match mode `any` or `all`
- at most 20 expected values, deduplicated after normalization
- expected values valid for the query type: IPv4 literal (A), IPv6 literal (AAAA), hostname (CNAME, NS), `host` or `pref host` (MX), any non-empty string ≤ 1024 bytes (TXT)

Built: the hostname must be a name, not an IP address (`internal/monitor/dnscheck`).

## Heartbeat

- positive expected interval
- non-negative grace
- token generated securely

Built bounds: expected interval 10 s to 24 h; grace 0 to 24 h.

## Status page

- unique slug
- hostname normalized/lowercase
- host mapping unique
- public display names required
- logo: PNG or JPEG only (sniffed), ≤ 512 KB, ≤ 1024 × 1024 px; SVG rejected
- unlisted token generated server-side only, never user-supplied
- a hostname cannot equal the instance's own base URL host

Built (`internal/statuspage`): slug of lowercase letters, digits and inner hyphens, at most 64 characters; at most 10 hostnames per page, each a plain DNS name or IP address without scheme, port or path; page password 8–128 characters; public history 1–365 days.

## Maintenance

- name: required, at most 100 characters
- start date and time required
- positive duration: 1 minute to 31 days
- recurrence: once, daily or weekly
- weekly recurrence requires weekday selection
- scope references existing monitors/tags (checked in the write transaction)

## Import

Import must be:
- validated completely before committing
- transactionally applied where practical
- clear about duplicate/conflict behavior (matching, `skip`/`replace` modes and mandatory preview: `15_CONFIG_BACKUP.md` "Config import")
- unable to smuggle secret fields into safe-import format unless explicitly supported

Import is not built yet (M9).
