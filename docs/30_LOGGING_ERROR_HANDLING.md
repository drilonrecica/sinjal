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

## Error taxonomy

Monitor failures should use stable kinds such as:
- timeout
- dns
- connect
- tls
- http_status
- body_assertion
- json_assertion
- permission
- protocol
- unknown

Store human-readable details separately.

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
