# Threat Model

What Sinjal protects, whom it trusts, and how each threat is met. The rules behind the mitigations are in `13_AUTH_SECURITY.md`; section names below refer to it unless another file is named.

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

Mitigations ("Password hashing", "Passkeys", "TOTP", "Sessions", "Re-authentication actions"):
- Argon2id
- passkeys
- optional TOTP
- secure sessions
- re-authentication
- no secrets in logs

### CSRF

Mitigation ("CSRF"):
- per-session/form CSRF tokens for browser state changes

### XSS

Mitigations ("Security headers", "Response data"):
- templ escaping
- avoid raw HTML injection
- CSP
- sanitize/escape diagnostic snippets

### SSRF abuse

Admin is intentionally allowed to monitor private networks.

Mitigations ("Internal monitoring / SSRF"):
- only admin configures monitors
- viewer/public cannot submit arbitrary destinations
- the form's Test now reaches no further than saving a monitor would: admin only, CSRF-protected, one check bounded to 20 s, 10 a minute per user, nothing stored
- status pages hide targets
- bounded redirects/body/timeouts

### Reverse-proxy spoofing

Mitigation ("Proxy trust"):
- explicit trusted proxies
- validate Host mapping

### Secret-at-rest theft

Mitigation ("Secrets at rest"):
- AEAD-encrypted sensitive fields
- separate master key file
- filesystem permissions

Note: if an attacker steals both the DB and the master key, or a full backup, secrets are recoverable. Full backups are therefore high-value secrets ("Backup implications").

### Backup path traversal

Restore must:
- reject archive entries escaping target directory
- validate expected file list/manifest
- avoid unsafe symlink extraction

See `15_CONFIG_BACKUP.md` and `37_BACKUP_FORMAT.md`.

### Untrusted remote response

Monitored endpoints may return malicious:
- huge body
- invalid encoding
- HTML/script
- redirect chain

Mitigations (`06_MONITORING_ENGINE.md`, "Response/body privacy"):
- body limit
- timeout
- no rendering response as raw HTML
- redirect limits
- escaped snippets

### Denial of service through configuration

Admin can create many monitors.

Mitigations (`07_SCHEDULER.md`, `18_PERFORMANCE.md`):
- bounded worker pool
- bounded queues
- body limit
- timeout
- operational soft limits/warnings

### Instance takeover during setup

A fresh instance reachable from the internet could be claimed by whoever opens `/setup` first.

Mitigations ("Initial setup"):
- one-time setup token logged at startup, required by `/setup`
- token kept in memory only and rotated on restart
- `/setup` returns 404 once an admin exists

### Session theft

Mitigations ("Sessions"):
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
