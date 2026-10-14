# Logging and Error Handling

## Logging

Default text logs should be concise and useful.

Include:
- timestamp
- level
- subsystem
- message
- relevant IDs

Never log:
- passwords
- TOTP secrets
- passkey private material
- master key
- Authorization headers
- webhook secrets
- full SMTP credentials
- heartbeat raw token

## Conventions

- Logging uses `log/slog` and writes to stderr. Format (`text` default, `json`) and level (`info` default) come from `SINJAL_LOG_FORMAT` / `SINJAL_LOG_LEVEL` (`internal/logging`).
- Every component logs through `logging.Sub(logger, "<subsystem>")`, which adds the `subsystem` attribute (for example `main`, `scheduler`, `results`, `http`).
- Sensitive values use `secret.String`, which prints `[REDACTED]` through fmt, slog and JSON. Config and model fields holding secrets must use it; call `Reveal()` only where the value is deliberately used.

## Error taxonomy

Monitor failures should use stable kinds such as:
- timeout
- dns
- connect
- tls
- http_status
- body_assertion
- json_parse
- json_assertion
- permission
- protocol
- dns_nxdomain
- dns_no_answer
- dns_mismatch
- dns_error
- heartbeat_missed
- unknown

Store human-readable details separately.

Built: the kind is stored in `check_results.error_kind`, the message in `error_message`. Which type produces which kind is listed per type in `06_MONITORING_ENGINE.md`. `unknown` is also the kind of a check cancelled by shutdown (never stored) and of a stored configuration that cannot be used (`07_SCHEDULER.md` "Running a check").

## UI errors

User-action errors:
- clear message
- field-level validation where possible
- no stack trace

System errors:
- banner/system diagnostics link
- correlation/log context if useful

## Panic policy

Recover HTTP-handler panics at the server boundary and log them.

Do not use panic for expected runtime failures.

A fatal startup configuration/migration error may terminate the process with a clear message.

Built:
- `middleware.Recover` logs a handler panic (request id, method, route, stack) and answers `500` unless the response has started; `http.ErrAbortHandler` keeps its `net/http` meaning
- the worker pool recovers a panic inside a check the same way, so one check cannot stop monitoring (`07_SCHEDULER.md` "Worker pool")
