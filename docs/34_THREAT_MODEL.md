# Threat Model

## Assets to protect

- admin account
- viewer accounts
- notification credentials
- monitor auth headers
- heartbeat tokens
- master encryption key
- private/internal monitor targets
- incident diagnostics
- full backups

## Trust assumptions

Trusted:
- administrator
- server filesystem administrator
- configured reverse proxy within trusted proxy boundary

Less trusted:
- viewer accounts
- status-page visitors
- public network
- monitored remote endpoints
- notification providers

## Primary threats

### Credential theft
Mitigations:
- Argon2id
- passkeys
- optional TOTP
- secure sessions
- re-authentication
- no secrets in logs

### CSRF
Mitigation:
- per-session/form CSRF tokens for browser state changes

### XSS
Mitigations:
- templ escaping
- avoid raw HTML injection
- CSP
- sanitize/escape diagnostic snippets

### SSRF abuse
Admin is intentionally allowed to monitor private networks.

Mitigations:
- only admin configures monitors
- viewer/public cannot submit arbitrary destinations
- status pages hide targets
- bounded redirects/body/timeouts

### Reverse-proxy spoofing
Mitigation:
- explicit trusted proxies
- validate Host mapping

### Secret-at-rest theft
Mitigation:
- AEAD-encrypted sensitive fields
- separate master key file
- filesystem permissions

Note:
If attacker steals both DB and master key/full backup, secrets are recoverable. Full backups are therefore high-value secrets.

### Backup path traversal
Restore must:
- reject archive entries escaping target directory
- validate expected file list/manifest
- avoid unsafe symlink extraction

### Untrusted remote response
Monitored endpoints may return malicious:
- huge body
- invalid encoding
- HTML/script
- redirect chain

Mitigations:
- body limit
- timeout
- no rendering response as raw HTML
- redirect limits
- escaped snippets

### Denial of service through configuration
Admin can create many monitors.

Mitigations:
- bounded worker pool
- bounded queues
- body limit
- timeout
- operational soft limits/warnings

### Session theft
Mitigations:
- TLS expected at reverse proxy
- Secure/HttpOnly
- expiry/rotation
- revoke security changes
- `__Host-` cookie prefix over HTTPS
- "Sign out other sessions" action

## Explicitly accepted risks

- An administrator can intentionally configure a monitor to contact private infrastructure.
- A host-level attacker with access to `/data` can likely access the master key and encrypted secrets.
- Sinjal is not a hardened multi-tenant SaaS isolation boundary.
