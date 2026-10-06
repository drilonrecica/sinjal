# Authentication and Security

## Admin authentication

Support:
- password
- optional passkey
- optional TOTP

Password + passkey is the normal recommended setup.

## Password hashing

Use Argon2id via a vetted library.

Store:
- algorithm identifier/version
- salt
- encoded parameters
- hash

Parameters should be benchmarked on target hardware and documented. Avoid weak defaults.

## Sessions

Policy (decision P0-09). Applies to admins and viewers.

Token:
- 32 random bytes from `crypto/rand`, base64url in the cookie
- only the SHA-256 hash is stored (`sessions.token_hash`)

Cookie:
- name `__Host-sinjal_session` when Secure; `sinjal_session` on plain HTTP (local/dev)
- Secure when served over HTTPS directly or via a trusted proxy
- HttpOnly
- `Path=/`, no `Domain`
- `SameSite=Lax`: Strict would drop the session when following links from notifications (email, Telegram, Discord) into the dashboard; CSRF tokens still protect every state change

Lifetime:
- absolute 30 days from login (`expires_at`); no sliding extension, no idle timeout
- `last_seen_at` is updated at most once per 5 minutes per session, to keep request handling free of DB writes
- expired sessions are rejected on read and deleted by the daily cleanup job

Rotation:
- a new token is issued on login and on re-authentication
- no periodic rotation

Invalidation:
- logout deletes the current session
- password change, TOTP reset/disable and passkey removal delete all **other** sessions of that user (the current one is rotated)
- disabling a user, deleting a user or changing their role deletes all of that user's sessions
- Settings → Authentication offers "Sign out other sessions" (requires re-authentication)

Sensitive actions require recent re-authentication:
- re-authenticate with password (plus TOTP when enabled) or a passkey
- valid for 10 minutes from `sessions.reauthenticated_at`
- a fresh login counts as re-authentication

## Re-authentication actions

At minimum:
- password change
- passkey deletion
- TOTP reset
- reveal/regenerate sensitive token
- instance reset
- restore backup
- encryption-key operations
- destructive mass deletion

## Passkeys

Use WebAuthn.

Requirements:
- explicit RP ID/base URL configuration
- clear error if reverse-proxy host config is wrong
- multiple credentials per admin allowed
- label credentials
- revoke credential

## TOTP

Optional.

Store encrypted secret.

Provide recovery/reset flow requiring admin re-authentication.

## Secrets at rest

Use a 32-byte random master key.

Default:
- create `/data/master.key`
- permissions 0600
- key never stored in SQLite

Use AES-256-GCM or another vetted AEAD available in Go.

For each encrypted value:
- random nonce
- versioned envelope format
- authenticated metadata as needed

Never reuse nonce with same key.

## Backup implications

A full disaster-recovery backup includes required key material and must be treated as highly sensitive.

A safe config export excludes secrets.

## CSRF

All state-changing browser actions must be protected.

HTMX does not remove CSRF requirements.

## Login protection

- bounded rate limiting
- generic failure messages
- audit failed attempts
- no user enumeration

## Proxy trust

Never trust all `X-Forwarded-*` headers unconditionally.

Configuration:
- explicit trusted proxy CIDRs/addresses or a clearly documented immediate-proxy mode

Host header/custom hostname routing must be validated.

## Internal monitoring / SSRF

Administrators are trusted to monitor private addresses.

Therefore:
- private network targets are allowed
- viewer accounts cannot create/edit monitors
- status pages must not expose targets
- redirects remain bounded by timeout/body limits
- no untrusted public endpoint can create arbitrary monitor requests

## Response data

Successful bodies: never persisted.

Failure snippets:
- small cap
- only as necessary
- not exposed publicly
- escape safely in UI

## Security headers

Set appropriate headers:
- Content-Security-Policy suitable for bundled assets
- X-Content-Type-Options
- Referrer-Policy
- frame restrictions
- permissions policy as appropriate

Avoid CDN dependencies that complicate CSP.

## Audit log

Record:
- login success/failure
- password/passkey/TOTP changes
- monitor create/delete
- notification config changes
- backup restore
- major security changes

Do not build compliance-grade immutable audit infrastructure.
