# Authentication and Security

## Admin authentication

Support:
- password
- optional passkey
- optional TOTP

Password + passkey is the normal recommended setup.

## Initial setup

Prevents a stranger who reaches a fresh instance first from creating the admin account (decision P0-10).

While no admin account exists:
- on every startup, generate a one-time setup token (128 bits from `crypto/rand`, base32)
- keep it in memory only; never store it in the database
- log it once at WARN level: `Initial setup: <SINJAL_BASE_URL or http://localhost:<port>>/setup?token=<token>`
- a restart generates a new token and invalidates the old one
- `/setup` requires the token (query parameter on GET, hidden form field on POST), compared in constant time
- failed attempts are rate-limited like login and return a generic error

Creating the admin:
- transactional and race-safe: it succeeds only if no admin exists at commit time
- writes an audit event
- discards the token

After an admin exists, `/setup` returns 404 and no token is generated or logged.

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

## Account recovery

There is no email or web-based recovery.

A locked-out admin (lost password, TOTP device or passkeys) recovers with `sinjal reset-admin` on the host or inside the container (see `15_CONFIG_BACKUP.md`). Access to the host/container and `/data` is the trust boundary; anyone with it can already read the database and master key.

Authentication state (users, password hashes, TOTP, passkeys, sessions) is always read from the database, never cached in process memory, so a CLI reset takes effect immediately.

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

### Master key lifecycle

Implemented in `internal/vault` and loaded at startup, after migrations and before the listener opens.

- Format: exactly 32 raw bytes (not hex/base64) in `<data dir>/master.key`. A symlink to a mounted secret is followed.
- Creation: only when the file does not exist **and** no `*_enc` column in the database holds a value. The key comes from `crypto/rand`, is written to a 0600 temp file and fsynced, then hard-linked into place. The link cannot overwrite an existing file. A WARN line asks the operator to back it up.
- Loading refuses to start, and changes nothing, when the file is not a regular file, has any group/other permission bit (the message says to run `chmod 600`), is not 32 bytes, or cannot be read.
- Missing key with encrypted data present: startup fails with instructions to restore `master.key` from backup. Sinjal never generates a replacement key (`19_RELIABILITY.md`).
- Encrypted columns are named `*_enc`; the presence check relies on this convention, so new tables are covered automatically.

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
