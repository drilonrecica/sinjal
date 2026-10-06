# Configuration Validation

## General

Reject invalid configuration at write/import time, not during monitor execution.

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

## TCP
- valid host
- port 1–65535

## ICMP
- valid hostname/IP

## DNS
- valid hostname
- supported record type
- resolver valid when provided (`host` or `host:port`, default port 53)
- match mode `any` or `all`
- at most 20 expected values, deduplicated after normalization
- expected values valid for the query type: IPv4 literal (A), IPv6 literal (AAAA), hostname (CNAME, NS), `host` or `pref host` (MX), any non-empty string ≤ 1024 bytes (TXT)

## Heartbeat
- positive expected interval
- non-negative grace
- token generated securely

## Status page
- unique slug
- hostname normalized/lowercase
- host mapping unique
- public display names required
- logo: PNG or JPEG only (sniffed), ≤ 512 KB, ≤ 1024 × 1024 px; SVG rejected
- unlisted token generated server-side only, never user-supplied
- a hostname cannot equal the instance's own base URL host

## Maintenance
- positive duration
- weekly recurrence requires weekday selection
- scope references existing monitors/tags

## Import

Import must be:
- validated completely before committing
- transactionally applied where practical
- clear about duplicate/conflict behavior (matching, `skip`/`replace` modes and mandatory preview: `15_CONFIG_BACKUP.md` "Config import")
- unable to smuggle secret fields into safe-import format unless explicitly supported
