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

### Implementation (M1-05)

- `serve` checks for an admin after migrations; if none exists it creates an `auth.SetupToken` (16 bytes, 26 lowercase base32 characters) and logs the setup link once at WARN after the listener opens. The access log records only the route pattern, so the token in the query string does not reach it.
- `/setup` (`internal/web/setup.go`) answers 404 when the process has no token, after the token is discarded, or when the database already holds an admin (checked on every request). Responses carry `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
- A wrong or missing token answers 403 with a generic page. Failures are counted per client IP (`proxy.ClientIP`): after 10 failures within 15 minutes the IP gets 429 for the rest of the window, even with the right token. The counter (`internal/ratelimit`) tracks at most 1024 IPs and evicts the oldest; login (M1-10) reuses it.
- `auth.CreateAdmin` validates the input, hashes the password outside the transaction, then in one transaction inserts the user with `INSERT … SELECT … WHERE NOT EXISTS (admin)` and the audit event `setup.admin_created`. Zero inserted rows means another request won: `ErrAdminExists`, answered with 404. Success discards the token and redirects (303) to `/login`.
- POST bodies are capped at 16 KiB. Until CSRF tokens exist (M1-08), the unguessable setup token in the form body is what stops cross-site submission.

### Credential policy

- Username: trimmed, 1–64 characters, no whitespace or control characters; case is kept.
- Password: at least 12 characters and at most 1024 bytes; no composition rules (NIST SP 800-63B). Owner decision during M1-05; `auth.ValidatePassword` applies it everywhere a password is set.

## Password hashing

Use Argon2id via a vetted library.

Store:
- algorithm identifier/version
- salt
- encoded parameters
- hash

Parameters should be benchmarked on target hardware and documented. Avoid weak defaults.

### Parameters (M1-04)

Implemented in `internal/auth` (`HashPassword`, `VerifyPassword`) with `golang.org/x/crypto/argon2`.

- Argon2id, v=19, **m=19456 KiB (19 MiB), t=2, p=1** (OWASP baseline), 16-byte salt from `crypto/rand`, 32-byte hash.
- Stored as a PHC string: `$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>` (unpadded standard base64). Known vectors in the tests were computed independently with OpenSSL (Node `crypto.argon2Sync`).
- Benchmark (`go test -bench . -benchmem ./internal/auth/`, Intel i5-7500 @ 3.4 GHz, Go 1.27): **~27 ms and 19 MiB allocated per hash or verify**. This is fast enough for logins and well above brute-force-relevant cost.
- RAM budget (`18_PERFORMANCE.md`): at most **2 hashes run at once** (a package semaphore), so a login burst allocates at most ~38 MiB on top of the idle footprint. The memory is transient: the Go runtime returns freed heap to the OS in the background. The idle budget is unaffected because hashing only happens during login and password changes. Login rate limiting (M1-10) keeps the semaphore from being a practical bottleneck.
- Verification uses the parameters stored in the hash, compares in constant time (`subtle.ConstantTimeCompare`), and rejects anything that is not a well-formed argon2id v=19 string (`ErrInvalidHash`, never a match). Stored parameters are bounded (m ≤ 256 MiB, t ≤ 16, p ≤ 8, salt 8–64 B, hash 16–64 B), so a tampered row cannot trigger a huge allocation.
- Rehash on login: `VerifyPassword` returns `needsRehash` when a correct password's stored parameters, salt length or hash length differ from the current ones. The login flow (M1-10) then stores a fresh `HashPassword`.
- If the parameters change, update this section with a new benchmark.

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

### Implementation (M1-06)

- `auth.Sessions` (`internal/auth/session.go`): `Create` (new token, `reauthenticated_at` = now), `Lookup`, `Rotate` (delete old + insert new in one transaction), `Delete`, `DeleteOthers`, `DeleteAllForUser`, `DeleteExpired`. `auth.SetPassword` updates the hash and deletes the user's other sessions in one transaction (`keepID` "" deletes all, for `reset-admin`).
- Tokens: 32 bytes from `crypto/rand`, unpadded base64url (43 characters) in the cookie; `sessions.token_hash` holds SHA-256 of the raw bytes. A cookie value that does not decode to 32 bytes is rejected without a query.
- `Lookup` joins `users` and rejects expired sessions (`expires_at <= now`) and disabled users. When `last_seen_at` is 5 minutes old or more it is updated with `WHERE last_seen_at = <old value>`, so concurrent requests write once; a failed update is logged and the request continues. `expires_at` never moves.
- Stored per session: `user_agent` (first 256 characters) and `ip_hint` (`proxy.ClientIP`), for the session list in Settings.
- Cookie (`internal/web/session.go`): `SetSessionCookie` / `ClearSessionCookie` choose `__Host-sinjal_session` + `Secure` when `proxy.IsHTTPS` (direct TLS or trusted proxy), else `sinjal_session`; always `HttpOnly`, `Path=/`, no `Domain`, `SameSite=Lax`, `Expires` = `expires_at`. Only the cookie name for the current scheme is read.
- `LoadSession` middleware puts the session and user into the request context; it enforces nothing (M1-11). An invalid cookie is cleared; a database error answers 500.
- `POST /logout` deletes the current session, clears the cookie and redirects (303) to `/login`. CSRF protection follows in M1-08.
- Cleanup: `serve` deletes expired sessions at startup and every 24 hours until shutdown; this moves into the daily job runner (M6-04).

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

### Secret envelope (v1)

`vault.Key.Seal` / `Open` store a value as `0x01 | nonce(12) | AES-256-GCM ciphertext | tag(16)`.

- The nonce is random per seal (`cipher.NewGCMWithRandomNonce`, `crypto/rand`). Random 96-bit nonces stay safe far beyond 2^32 seals per key.
- AAD is the version byte followed by the storage context (table, column, row id), each length-prefixed. A value copied to another row or column, or a changed version byte, fails authentication.
- Errors: `ErrMalformed` (too short), `ErrUnknownVersion`, and `ErrDecrypt`. `ErrDecrypt` is one error for tampering, a wrong context and a wrong key, so no detail leaks.
- A future format gets a new version byte. `Open` keeps accepting v1 until every value has been re-sealed.

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

### Implementation (M1-07)

`internal/web/proxy` resolves every request once (router middleware, right after the request ID) into client IP, scheme and host. All code reads them through `proxy.ClientIP`, `proxy.IsHTTPS` and `proxy.Host`, never `RemoteAddr`, `r.TLS` or `r.Host` directly.

- `SINJAL_TRUSTED_PROXIES` lists CIDRs or bare IPs. Empty (the default) trusts no proxy. There is no "trust the immediate peer" mode.
- The TCP peer must be inside a trusted prefix for any `X-Forwarded-*` header to count. Otherwise: client IP = peer, scheme = `https` only for a direct TLS connection, host = `Host` header.
- `X-Forwarded-For` (all header lines joined) is walked right to left, skipping trusted hops; the first untrusted address is the client. A malformed entry ends the walk at the last valid hop. If every hop is trusted, the leftmost one is used.
- `X-Forwarded-Proto` and `X-Forwarded-Host`: the rightmost value (set by the immediate proxy) is used. Proto must be `http`/`https`; host may contain only letters, digits, `.`, `-`, `:` and IPv6 brackets. Invalid values are ignored.
- The RFC 7239 `Forwarded` header is not read.
- IPv4-mapped IPv6 peers and prefixes are normalised to IPv4; IPv6 zones are dropped.
- Without the middleware (for example a handler under test), the helpers trust no proxy.
- The access log records the resolved `client_ip`.

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
