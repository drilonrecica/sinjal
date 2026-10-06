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
