# Decision Log

This summarizes the locked design rounds.

## Product
- Personal power tool, not broad Kuma replacement.
- Must look excellent.
- Strongly optimized, not pathological micro-optimization.
- Admin dashboard authenticated; status pages may be public/private/unlisted/passworded.
- Password + passkey, optional TOTP.
- DB/UI config with YAML/JSON export.
- tiered aggregation.
- HTTP/TCP/ICMP/DNS + heartbeat.
- SMTP/Telegram/Discord/webhook.
- conceptual, not compatibility, relationship to Uptime Kuma.

## Architecture
- native binary + first-class Docker.
- central scheduler + bounded workers.
- jitter.
- WAL + batched dedicated result writes.
- HTMX + SSE + small vanilla JS.
- four designed themes.
- env/CLI for runtime, SQLite for app state.
- built-in backup/export.
- system diagnostics.

## Incident behavior
- 30s default.
- two failures.
- 5s confirmation retry.
- one success recovers.
- flapping detection.
- one-time/recurring maintenance.
- raw + adjusted uptime.
- HTTP default 200–399.
- text + JSON assertions.
- parent/child suppression.

## Status pages
- multiple pages.
- public/authenticated/password/unlisted.
- viewer accounts and page passwords allowed.
- operational summary only by default.
- theme/logo/title/description/accent.
- native hostname mapping.
- 30d default incident history.
- manual incident message.
- groups.
- JSON + RSS/Atom.

## Notifications
- reusable profiles.
- info/warning/critical.
- per-severity routing.
- quiet hours per profile.
- one unresolved reminder.
- useful operational detail.
- recovery duration.
- channel health.
- safe template variables.
- test/simulated incident.
- channel configuration is one typed value per channel, sealed as a whole; secrets are write-only and a secret follows its name field (M5-02, `11`).

## Data
- raw 7d.
- 5m to 30d.
- hourly to 1y.
- daily forever.
- incidents forever.
- current/avg/min/max/p95.
- separate latency/availability views.
- tiny chart library.
- presets + custom range.
- descriptive tags.
- saved views.
- daily cleanup.

## Security
- password + passkey.
- optional TOTP.
- encrypted secrets.
- auto-generated master key.
- 30d sessions.
- re-auth for sensitive actions.
- explicit proxy trust.
- private targets allowed for trusted admin.
- no full response body persistence.
- basic audit log.

## UX
- hybrid overview.
- color + icon + text.
- comfortable/compact density.
- command palette actions.
- limited shortcuts.
- progressive monitor form.
- detail tabs.
- genuine theme personalities.
- four themes.
- minimal functional motion.

## Operations
- Docker first docs/native supported.
- manual app upgrades.
- automatic backup before migration.
- no reverse migrations.
- daily local backup.
- safe + full backup distinction.
- Docker healthcheck.
- one `/data`.
- text logs default/JSON optional.
- pre-1.0 API/config may change but migrations stay safe.

## Final scope
- polished narrow v1.
- hard non-goal list.
- no browser automation.
- heartbeat but no general remote agents.
- small documented API.
- Kuma importer later at most.
- explicit performance budgets.
- dependency budget.
- public source/issues open/PRs not accepted.
- distinctive standalone name.

## Branding
- Sinjal.
- serious but indie.
- indirect monitoring meaning.
- no need to buy domain.
- GitHub Pages/personal page sufficient.
- product branding slightly visible on status pages.
- no mascot.
- factual descriptor + stronger tagline.

## Technical
- chi.
- modernc SQLite.
- handwritten SQL.
- embedded numbered SQL migrations.
- min-heap scheduler.
- automatic worker default with override.
- pooled HTTP clients keyed by config.
- result channel + dedicated processor.
- feature-oriented packages.
- balanced test strategy.

## Reliability
- bounded SQLite backoff.
- degraded/read-only on corruption where possible.
- disk warnings.
- explicit network timeout.
- body cap.
- durable incidents.
- UTC persistence + monotonic runtime.
- connectivity root monitor.
- bounded notification retries.
- support 1,000 monitors; not 10,000 target.

## Repository
- MIT.
- issue templates.
- PRs auto-closed/not accepted.
- explicit AI PR policy.
- SemVer.
- manual builds/uploads.
- amd64 + arm64.
- checksums/SBOM/signatures.
- README + docs.
- strict AGENTS.md.

## Amendments (P0 decisions)
- P0-01: no `DEGRADED` monitor state; TLS expiry is a warning indicator alongside UP; assertion failures are ordinary failures.
- P0-02: schema gaps closed (user UI prefs, per-user saved views, published manual notes, incident notification state + maintenance overlap, single active incident index, TLS warning dedupe, heartbeat source label, flapping_since); one migration per milestone.
- P0-03: Docker runtime base is `scratch` + copied CA bundle + embedded tzdata; non-root UID 65532; `sinjal healthcheck` subcommand for `HEALTHCHECK`.
- P0-04: ICMP hand-written on `x/net/icmp`; unprivileged datagram sockets first, raw-socket fallback, explicit `permission` failure; never privileged containers.
- P0-05: approved dependency list in `docs/40_DEPENDENCIES.md` (chi, modernc sqlite, templ, x/crypto, x/net, go-webauthn, rsc.io/qr, go.yaml.in/yaml/v3; vendored htmx+SSE, uPlot, Lucide subset); TOTP hand-written on stdlib.
- P0-06: self-host Inter + JetBrains Mono as variable woff2 subset to Latin + Latin Extended-A; ≤150 KB font budget; system fallbacks; no CDN.
- P0-07: JSON assertions use a strict path subset (`$`, `.name`, `["key"]`, `[n]`); typed scalar expected values; type-aware equality; `not equals` requires the path to exist.
- P0-08: FLAPPING is an overlay (`flapping_since`) on the real state; enter at ≥4 confirmed transitions in 10 min, exit after 10 min without one; one warning on entry, DOWN or STABLE on exit; window rebuilt from incidents after restart.
- P0-09: sessions use a 32-byte token (SHA-256 stored), `__Host-` cookie when Secure, SameSite=Lax, absolute 30 d lifetime, `last_seen_at` throttled to 5 min, re-auth valid 10 min, other sessions revoked on security changes.
- P0-10: `/setup` requires a one-time in-memory setup token logged at startup while no admin exists; rotated on restart; 404 after setup.
- P0-11: account recovery is `sinjal reset-admin` (new printed password, TOTP cleared, sessions revoked, passkeys optionally removed, audited); host access is the trust boundary; no web recovery.
- P0-12: uptime is time-weighted from incidents and pause intervals (new `monitor_pauses`); adjusted uptime also removes excluded maintenance time; no data on zero denominator; truncated display; pausing closes the active incident silently.
- P0-13: DNS expected values use a per-monitor match mode (`all` default, `any`) with normalization of names, IPs, MX and TXT; NXDOMAIN/empty answers always fail.
- P0-13 amendment (M4-04): the DNS check speaks DNS itself with `x/net/dns/dnsmessage` instead of `net.Resolver`, which cannot tell NXDOMAIN from an empty answer and consults `/etc/hosts` and search domains; resolver from config or `/etc/resolv.conf`.
- P0-14: browser tests use chromedp in a separate dev-only `tests/browser` module, run via `make test-browser`, never in the product module; dev-only tools listed in 40.
- P0-15: status pages show a 90-day adjusted uptime strip; unlisted URLs are `/s/{token}` (hashed, shown once, noindex); logos PNG/JPEG only; mapped hostnames serve only that page's public routes.
- P0-16: config import matches by name/key/slug, modes `skip` (default) and `replace`, never deletes, mandatory dry-run preview, all-or-nothing transaction; secrets never imported.
- P0-17: releases are signed with `ssh-keygen -Y sign` using a dedicated Ed25519 release key over `checksums.txt`; SBOM via syft as SPDX JSON; public key in `docs/release-signing/allowed_signers`.
- P0-18: a check-only GitHub Actions workflow (fmt, vet, lint, race tests, stale templ check) on push to master; read-only permissions, SHA-pinned actions, no secrets, never publishes.
- P0-20: real notification delivery is verified manually with owner test accounts supplied only via a local git-ignored `.env` (`SINJAL_TEST_*`); never in CI.
- P0-21: logo is the "open ring" mark (continuity ring with one gap and an accent pip) plus an outlined lowercase Inter SemiBold wordmark; files in `docs/brand/`.
- M5-09/M5-10: an outage reminder or TLS threshold that falls due while maintenance, a down parent or flapping (reminder only) suppresses it is recorded and not sent later, as quiet hours do; the next TLS threshold still warns.
- M5-11: "Send test notification" redirects to the channel's edit page with the outcome (Post/Redirect/Get), so a reload never sends again; "Simulate incident" renders its result in the POST response because it stores nothing to redirect to (the browser asks before resubmitting).
- M6-05: the 1-year History range may take about 55 ms to read (≈5 ms over the 50 ms page target): the cost is the driver handing over the rows a year leaves after retention, and no measured alternative (SQL `GROUP BY`, a separate p95 query, `WITHOUT ROWID`) was faster. Accepted by the owner (2026-10-07) for the rarest range; 90 days and shorter stay within budget.
