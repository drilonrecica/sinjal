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
- a credential change (password change, turning TOTP on or off, adding or removing a passkey) deletes all **other** sessions of that user (the current one is rotated)
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
- Cleanup: the daily job (`internal/jobs`, `09_DATABASE.md` "Retention jobs") deletes expired sessions at 04:00 local and at startup when that run is overdue.
- Event stream: `GET /events` stays open long after the request that passed `RequireAuth`, so it repeats `Lookup` every 20 s and ends when the session is gone (logout, expiry, rotation, disabled viewer); see `32_SSE_EVENTS.md`.

## Authorization

### Implementation (M1-11)

Two roles: `admin` changes everything, `viewer` only reads. Enforcement is by placement in the route table (`web.Routes`):

- session group (`LoadSession`, CSRF): `/setup`, `/login`, `POST /logout` are public;
  - `RequireAuth` group: every page; anonymous page requests get 303 to `/login?next=<path>` (no `next` for `/`), other methods 401, htmx requests 401 with `HX-Redirect`;
    - `RequireAdmin` group: every state-changing app route. A viewer gets 403 ("Your account can view Sinjal but not change it."), logged at WARN.
- Pages render with the signed-in user's theme and density.
- `TestRouteTableGuards` walks the production table with `chi.Walk` and probes every route: non-public pages must redirect anonymous users, non-public state changes must answer 401 anonymously and 403 to a viewer with a valid CSRF token. The only exceptions are the explicit `publicRoutes` and `viewerMutations` lists in the test (own session/account actions: re-authentication and changing one's own password). `TestRouteTableGuardsCatchOmissions` mounts unguarded routes and shows the check reports them.

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

### Implementation (M1-12)

- `auth.ReauthWindow` = 10 minutes; `Session.RecentlyAuthenticated(now)` compares with `sessions.reauthenticated_at`, which login and every re-authentication set.
- `web.RequireRecentAuth(logger, now)` is mounted inside `RequireAuth` on sensitive routes. A stale page request gets 303 to `/reauth?next=<path>`. A stale POST cannot be replayed, so it returns to the page the form was on: the `Referer` when it is Sinjal's own origin (`Referrer-Policy: same-origin` keeps it), otherwise no `next`. htmx requests get 403 with `HX-Redirect`. Without a session it fails closed (401).
- `/reauth` (behind `RequireAuth`, open to viewers): a password form for the signed-in user. `auth.Authenticator.Reauthenticate` uses the same verification path as login (dummy hash, one Argon2 per attempt) and audits `auth.reauthenticated` / `auth.reauth_failed`. Failures share the login limiter, keyed by client IP and user ID (10 per 15 minutes, then 429).
- Success rotates the session (`Sessions.Rotate`): a new token, `reauthenticated_at` = now, and a new 30-day lifetime, since re-authentication proves the same credentials as a login. The old cookie and its CSRF token stop working. Redirect: 303 to `safeNext(next)`.
- TOTP and passkey management (M1-13, M1-14) are the production users of `RequireRecentAuth`; password change and session sign-out (M1-16…M1-18) follow. The `/reauth` page also offers "Use a passkey instead" when the user has one; a passkey alone is enough, since it verifies the user itself. When the account has TOTP enabled, `/reauth` asks for the code together with the password and both must be right (the code is checked only after the password, so a wrong password does not use it up).
- Heartbeat tokens (M4-06): regenerating one (`POST /monitors/{id}/heartbeat/token`) is behind `RequireRecentAuth`. Creating a heartbeat monitor is not (owner decision): the creating admin is shown a token that did not exist before, and a stale create would bounce the typed form. Either way the token is shown once, in that response, and only its hash is kept.

## Passkeys

Use WebAuthn.

Requirements:
- explicit RP ID/base URL configuration
- clear error if reverse-proxy host config is wrong
- multiple credentials per admin allowed
- label credentials
- revoke credential

### Implementation (M1-14)

Admins only. `github.com/go-webauthn/webauthn` v0.18.2 behind `internal/auth/passkey.go`; ceremonies and state in `internal/web/passkey.go`; browser side in `web/static/js/passkey.js`.

Configuration:
- The relying party ID is the host of `SINJAL_BASE_URL`; the only accepted origin is its scheme and host (with port). Nothing is derived from request headers.
- Passkeys are unavailable, and only passkeys, when `SINJAL_BASE_URL` is unset, is an IP address, or is plain `http` on a host other than `localhost` (browsers refuse WebAuthn there). Startup logs the reason once (INFO when unset, WARN otherwise) and Settings → Authentication shows it.
- Every ceremony first compares the request's origin (trusted-proxy rules) with the configured one. A mismatch, the usual reverse-proxy mistake, answers 400 with a message naming both addresses and pointing at `SINJAL_BASE_URL` and `SINJAL_TRUSTED_PROXIES`, and is logged at WARN.
- Attestation preference `none`, no metadata service: no outbound calls.

Policy (owner decision, 2026-10-06): a passkey signs in on its own, without username, password or TOTP code. So credentials must be discoverable (resident key required) and every ceremony requires user verification (PIN or biometric). Security keys without a PIN cannot be registered.

Ceremonies (JSON, two requests each):
- Sign-in: `POST /login/passkey/begin` and `/finish`, public. The user is found from the credential's user handle (the account ID) and must not be disabled. Success creates a session like a password login. The login page shows the button only when passkeys are available and at least one is registered.
- Re-authentication: `POST /reauth/passkey/begin` and `/finish`, for the signed-in user's own credentials only. Success rotates the session.
- Registration: `POST /settings/authentication/passkeys/begin` and `/finish`, admin and recent re-authentication. The existing credentials are excluded, so one authenticator is not registered twice; a `UNIQUE` index on the credential ID is the hard rule. Labels are 1–64 characters ("Passkey" when empty); at most 20 passkeys per account.
- Revoke: `POST /settings/authentication/passkeys/{id}/delete`, admin and recent re-authentication, limited to the caller's own passkeys.

Ceremony state: the challenge and what it was issued for (kind, user, label, return path) are kept in process memory for 5 minutes, in a map bounded to 256 entries (oldest dropped). The browser holds only a random ID in an `HttpOnly`, `SameSite=Strict` cookie (`__Host-sinjal_passkey` over HTTPS). Finishing removes the entry, so a captured answer cannot be replayed, and a ceremony can only be finished as what it was begun as. A restart only means starting a ceremony again. Users and credentials are still read from the database at finish, so `reset-admin --remove-passkeys` takes effect immediately.

Stored per credential (`passkeys`): credential ID, public key, signature counter, transports, the backup-eligible flag from registration, label, `created_at`, `last_used_at`. The backup-eligible flag must be the same on every later assertion.

Signature counter: stored after each use. A counter that does not increase while either side is non-zero means the credential may have been cloned: the ceremony is refused, logged at WARN and audited as a failure. Counters that stay at zero (synced passkeys) are normal.

Failures: every rejected response is the same generic 401 to the client; the reason is logged at INFO. Failed sign-ins count per client IP, failed re-authentications per client IP and user, in the login limiter (10 per 15 minutes, then 429).

Audit events: `auth.passkey_added`, `auth.passkey_removed`; sign-in and re-authentication use the password events with `"factor":"passkey"`.

Sessions: adding or removing a passkey deletes the user's other sessions and rotates the current one.

Tests use a software authenticator (`internal/auth/passkeytest`, ES256, attestation `none`), which is not linked into the binary.

## TOTP

Optional.

Store encrypted secret.

Recovery is the host-side `sinjal reset-admin` command (see *Account recovery*); there is no web flow.

### Implementation (M1-13)

Admins only. Hand-written on the standard library (`internal/auth/totp.go`, `40_DEPENDENCIES.md`).

- Algorithm: RFC 6238 with HMAC-SHA1, 6 digits and 30-second steps, the parameters every authenticator app supports. The current step and one step either side are accepted; all three candidates are compared in constant time. A code may be typed with a space ("123 456"). Tested against RFC 4226 Appendix D and RFC 6238 Appendix B.
- Secret: 20 random bytes, stored only as a v1 envelope in `users.totp_secret_enc` (AAD `users` / `totp_secret_enc` / user id). It is shown once, during enrolment, and never logged.
- Replay prevention: `users.totp_last_step` holds the last accepted time step. A code counts only if `UPDATE … WHERE totp_last_step IS NULL OR totp_last_step < <step>` changes the row, so the same code, an older code, or the same code in two concurrent requests is accepted at most once.
- Enrolment (`GET /settings/authentication/totp`, recent re-authentication required): each load creates a new secret and shows it as a QR code (`rsc.io/qr`, inline PNG data URI), as a base32 setup key and as the `otpauth://totp/Sinjal:<login>?secret=…&issuer=Sinjal&algorithm=SHA1&digits=6&period=30` link. Nothing is stored yet: the secret travels in a hidden form field, encrypted with the master key, bound to the user and valid for 10 minutes. `POST` with a valid code stores the secret and marks the code's step as used, in one transaction with the session revocation and the audit event `auth.totp_enabled`.
- Disable (`POST /settings/authentication/totp/disable`, recent re-authentication required, which itself needs a code): clears both columns, audit event `auth.totp_disabled`. Reset is disable followed by a new enrolment. A lost device is recovered with `sinjal reset-admin`.
- Login: a correct password for an account with TOTP does not sign in. `/login` answers with the code form, which carries a challenge: the user id and a 5-minute expiry, encrypted with the master key. `POST /login/totp` checks the challenge and the code, then creates the session. `auth.login_succeeded` is written only then; a wrong code is `auth.login_failed`, both with `"factor":"totp"`.
- Rate limit: wrong login codes share the login limiter, counted per account from any address (10 per 15 minutes, then 429). Guessing a code needs the password first, so this cannot be used to lock out an account whose password is unknown, and spreading guesses over many addresses does not help. Re-authentication failures (password or code) stay keyed by client IP and user.
- Sessions: turning TOTP on or off deletes the user's other sessions and rotates the current one.

### Implementation (M1-16)

Viewer accounts (`internal/auth/users.go`, `internal/web/settings_users.go`, `internal/web/account.go`).

- Admin only, in Settings → Authentication, behind recent re-authentication: `POST …/viewers` (login, password, confirm; same `NormalizeLogin` and `ValidatePassword` as setup; a login already taken, compared case-insensitively, is an inline error), `POST …/viewers/{id}/disable` and `…/enable`. No invitations: the admin chooses the password and hands it over.
- Disabling deletes all of the viewer's sessions in the same transaction; a disabled account cannot sign in (generic credential error) and `Authenticator.lookup` also rejects its sessions. Only `role = 'viewer'` rows can be disabled, so an admin cannot be locked out from the web; unknown ids and admins are 404.
- `GET/POST /account/password` is open to every signed-in user and listed in `viewerMutations`. Recent re-authentication is the proof of the current password (it is asked before the form is shown, so a typed password is not lost to a redirect). `auth.ChangePassword` stores the hash, deletes the user's other sessions and audits `auth.password_changed` in one transaction; the handler then rotates the current session.
- Audit events: `user.viewer_created`, `user.viewer_disabled`, `user.viewer_enabled` (actor = admin, object = viewer), `auth.password_changed`. No password is ever in the metadata.
- Viewers cannot reach Settings → Authentication, so they never see passkey, TOTP or viewer controls; `auth.ListViewers` reads no credential column.

## Account recovery

There is no email or web-based recovery.

A locked-out admin (lost password, TOTP device or passkeys) recovers with `sinjal reset-admin` on the host or inside the container (see `15_CONFIG_BACKUP.md`). Access to the host/container and `/data` is the trust boundary; anyone with it can already read the database and master key.

Authentication state (users, password hashes, TOTP, passkeys, sessions) is always read from the database, never cached in process memory, so a CLI reset takes effect immediately.

### Implementation (M1-15)

`auth.ResetAdmin` (`internal/auth/reset.go`), called by `sinjal reset-admin` (`cmd/sinjal/reset.go`). One transaction: choose the admin (`--login`, or the only admin; several admins without `--login` is an error, and a viewer is never a target), store a new Argon2id hash of a 24-character random password, clear `totp_secret_enc` and `totp_last_step`, delete all of the user's sessions, delete passkeys only with `--remove-passkeys`, and write the audit event `admin_reset_cli` (metadata `passkeys_removed`, never the password). The password is the only line on stdout; the guidance goes to stderr. The command needs neither migrations nor the master key, and it refuses to create a database when `sinjal.db` is missing.

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

### Implementation (M1-08)

`internal/web/csrf.go`, mounted by `web.Routes` on the session group (after `LoadSession`). Every request in that group except GET/HEAD/OPTIONS passes two checks:

1. **Origin.** `Sec-Fetch-Site` must be `same-origin` or `none` when present (`same-site` is refused: a sibling subdomain is not trusted). Without it, `Origin` must equal `<scheme>://<host>` as resolved by the trusted-proxy rules (`proxy.IsHTTPS`, `proxy.Host`); `Origin: null` is refused. A request with neither header is not from a browser and passes this step. This is the model of Go's `http.CrossOriginProtection`, which cannot be used directly because it compares with `r.Host`.
2. **Synchronizer token**, when the request has a session: `X-CSRF-Token` header, or else the `_csrf` field of a url-encoded body (capped at 64 KiB), must equal `base64url(HMAC-SHA256(k, session id))`, compared with `hmac.Equal`. The token is never in the query string.

- `k` is `vault.Key.Derive("sinjal csrf v1")` (HKDF-SHA256 over the master key), so tokens survive restarts and need no storage. A token changes when its session is rotated (login, re-authentication); open tabs from before then need a reload.
- Anonymous requests (`/login`, `/setup`) only get the origin check; the setup form additionally carries its setup token.
- Rejection: 403 with a generic "This form has expired" page, and a WARN log with the reason and client IP (never the token).
- Delivery: `templates.Page.CSRFToken` (set by `pageFor`) renders `hx-headers='{"X-CSRF-Token":…}'` on `<body>` for htmx, and `templates.CSRFField` the hidden input for plain forms.
- Multipart bodies are not parsed for the token; the first upload form (M7 logo, M9 restore) must send it in the header or extend the middleware.
- Exemption is by placement: health checks, static assets and machine endpoints (heartbeat push) are mounted outside the session group.

## Login protection

- bounded rate limiting
- generic failure messages
- audit failed attempts
- no user enumeration

### Implementation (M1-10)

- `auth.Authenticator.Login` (`internal/auth/login.go`) looks the account up by its trimmed login (case-sensitive, as stored). An unknown login, a disabled account and an account without a password are verified against a dummy hash (created once per process), so every failure costs one Argon2 verification and returns the same `ErrInvalidCredentials`. Passwords over 1024 bytes fail without hashing (none was ever accepted). A malformed stored hash is logged and treated as a failure.
- A correct password whose hash uses outdated parameters is rehashed (`UPDATE … WHERE password_hash = <old>`, so a concurrent password change wins; a failure is only logged).
- Audit events: `auth.login_succeeded` and `auth.login_failed`, with `user_id` when the login exists and `{"client_ip": …}` as metadata. The attempted login string is never stored, because people type passwords into it by mistake. Every event goes through `audit.Write` (see M1-17).
- `/login` (`internal/web/login.go`): GET redirects a signed-in user onward; POST (16 KiB body cap) answers 401 with "Incorrect username or password." for every failure. Success deletes any session the browser already had, creates a new one (a fresh login counts as re-authentication) and redirects with 303 to `next` when it is a same-origin path (`safeNext`: no scheme/host, no `//` or `/\` prefix, no control characters or backslashes), otherwise to `/`.
- Rate limit: 10 failed checks per (client IP, lower-cased login) per 15 minutes; then 429 with `Retry-After` and no hash computed, even for the right password. The limiter (`internal/ratelimit`) holds at most 4096 keys and evicts the oldest. A success resets the key. Blocked attempts are logged, not audited.
- CSRF: an anonymous login POST gets the origin check (M1-08), which also stops login CSRF.

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

### Implementation (M1-09)

`middleware.SecurityHeaders` runs in `NewRouter` right after the request ID and sets the headers before the handler, so pages, static files, 404s, CSRF rejections and recovered panics all carry them:

- `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`
- `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` (`/setup` overrides with `no-referrer`), `X-Frame-Options: DENY` (for old browsers; `frame-ancestors` is the real control), `Cross-Origin-Opener-Policy: same-origin`
- `Permissions-Policy` disables camera, microphone, geolocation, payment, USB, serial, MIDI, display capture and topics. WebAuthn keeps its default (self) for passkeys.

Notes:
- There is no inline script, so no hash is needed: the theme is rendered on the server as `data-theme`/`data-density`, which also avoids a flash of the wrong theme.
- htmx is configured with `<meta name="htmx-config">`: `includeIndicatorStyles:false` (its injected `<style>` would be blocked; the indicator rules are in `base.css`), `allowEval:false`, `allowScriptTags:false`.
- `templates.TestPagesNeedNoInlineCode` renders every page and fails on inline `<script>`, `<style>`, `style=`, `on*=` handlers or `javascript:` URLs. New pages must be added to it.
- No HSTS: TLS terminates at the reverse proxy, which owns that decision for its domain.
- Status pages (M7) may need their own policy (accent colour, embedding); that is decided there.

## Audit log

Record:
- login success/failure
- password/passkey/TOTP changes
- monitor create/delete
- notification config changes (`notification.channel_created`, `_updated`, `_deleted`; metadata is the name and type, never configuration), test sends (`notification.channel_tested`, with `result` sent or failed), profile changes (`notification.profile_created`, `_updated`, `_deleted`) and simulations (`notification.profile_simulated`), metadata the profile's name
- backup restore
- major security changes

Do not build compliance-grade immutable audit infrastructure.

### Implementation (M1-18)

Auth UI (`web/templates/*.templ`, `web/static/css/{auth,settings,audit}.css`). Every page is server-rendered with no inline script or style; colours come from tokens, so all four themes apply.

- Forms: a visible label on every input (`TestEveryInputHasALabel`), field errors next to the field with `aria-invalid` and `aria-describedby` (`fieldAttrs`), and for the one generic failure of login, TOTP and re-authentication an alert (`id="form-error"`, `role="alert"`) that the fields point at (`formErrorAttrs`). Passwords are never echoed. Inputs and buttons are at least 2.75 rem tall.
- Pre-login pages sit in a bordered card; the settings pages share a navigation strip (`settingsPages`): Your account for everyone, Authentication and System for admins only (`Page.Admin`). The current page is marked by weight and a bar as well as colour.
- `/account/password` is "Your account": change password and **Sign out other sessions** (`POST /account/sessions/sign-out-others`, recent re-authentication, `auth.SignOutOtherSessions` deletes the user's other sessions and audits `auth.sessions_revoked` with the count). Open to viewers.
- Settings → Authentication: account link, authenticator app, passkeys, viewers. Actions that remove access (turn off, remove, disable) use the danger colour with a text label.
- Checked in Chromium against the built binary: setup, sign-in failure, Authentication, System and Your account, desktop and 390 px wide, with no CSP violation (only the unrelated `/favicon.ico` 404).

### Implementation (M1-17)

- `internal/audit` is the one writer: `audit.Write(ctx, q, Event, now)` takes a `*sql.DB` or a `*sql.Tx`, so an event joins the transaction of the change it records (a rolled-back change leaves no event); `audit.Record` writes on its own with the busy retry. Event types are constants in the package (`audit.LoginSucceeded`, …); a later milestone adds its own next to them. The auth package's private writer is gone.
- Written today: `setup.admin_created`, `admin_reset_cli`, `auth.login_succeeded|login_failed` (password, TOTP and passkey factors), `auth.reauthenticated|reauth_failed`, `auth.password_changed`, `auth.totp_enabled|totp_disabled`, `auth.passkey_added|passkey_removed`, `user.viewer_created|viewer_disabled|viewer_enabled`. `monitor.created` (M2-17), `monitor.paused|resumed|deleted` (M2-18; pause and resume only when they changed something), `monitor.heartbeat_token_regenerated` (M4-06), `maintenance.created|updated|deleted` (M3-07, metadata the window's name). Notification and backup events come with their milestones. Metadata is a few string pairs (`client_ip`, `factor`); it never holds a password, token, secret or an attempted login.
- `GET /settings/system` shows the log read-only, newest first, 50 per page with an "Older events" link (`?before=<id>` keyset paging on the primary key: every page costs the same and new events never shift an older page; a bad cursor is the first page). Admin only, because the log carries client addresses. `audit.List` joins the actor's login; a deleted actor leaves the event with no name (`ON DELETE SET NULL`).
- There is no deletion, export or retention for the log yet; retention belongs to the daily job runner (M6-04).
- Monitor events (M2-17, M2-18) are written by the monitor handlers with `audit.Record` right after the committed change, with the monitor's name as metadata; the store functions take no actor. A failed audit write is logged at ERROR and the change stands. Edits are not audited (this section lists create and delete).

### Monitor secrets in the form (M2-17)

The create/edit page (`/monitors/new`, `/monitors/{id}/edit`, admin only) never renders a secret value. Basic-auth user and password, the bearer token and secret header values are password inputs that always render empty; when a value is stored the field says "Set. Leave blank to keep the current value." Blank keeps, a filled field replaces, a Remove box deletes a secret header, and choosing "None" (or the other method) deletes the stored credentials. Values go straight to `store.SetSecret` (encrypted, `monitor_secrets`); a rejected form does not echo them, and they are not logged. Credentials in the URL and `Authorization` / `Proxy-Authorization` / `Host` as plain or secret headers are refused, so credentials always end up encrypted.
