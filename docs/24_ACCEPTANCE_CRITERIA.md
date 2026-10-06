# Acceptance Criteria

Sinjal v1 is complete only when these criteria are satisfied.

## Deployment
- [ ] Runs as one native binary.
- [ ] Runs as one Docker container.
- [ ] Uses one `/data` directory.
- [ ] No external DB/cache/queue is required.
- [ ] `/healthz` and `/readyz` work.
- [ ] Linux amd64 and arm64 builds work.

## Authentication
- [ ] Initial admin setup is secure.
- [ ] Password login works.
- [ ] Passkey enrollment/login works.
- [ ] Optional TOTP works.
- [ ] Viewer role is read-only.
- [ ] Sessions persist safely.
- [ ] Sensitive actions require re-authentication.
- [ ] Login rate limiting works.

## Secrets
- [ ] `/data/master.key` created securely.
- [ ] Secret fields are encrypted in SQLite.
- [ ] Secrets never appear in normal logs.
- [ ] Safe config export redacts secrets.
- [ ] Full backup can restore secrets.

## Monitoring
- [ ] HTTP(S) works.
- [ ] TCP works.
- [ ] ICMP works or clearly explains missing permission.
- [ ] DNS works for documented record types.
- [ ] heartbeat works.
- [ ] default interval is 30s.
- [ ] timeout enforced.
- [ ] first failure triggers 5s retry.
- [ ] second failure marks DOWN by default.
- [ ] one success recovers by default.
- [ ] HTTPS TLS expiry warnings work.
- [ ] status assertions work.
- [ ] text assertions work.
- [ ] JSON assertions work.
- [ ] response bodies are capped.
- [ ] successful bodies are not persisted.

## Scheduling
- [ ] central priority scheduler.
- [ ] bounded worker pool.
- [ ] jitter prevents synchronized burst.
- [ ] no permanent goroutine per monitor.
- [ ] graceful shutdown flushes safely.
- [ ] active incident survives restart.

## Incidents
- [ ] incident opens only after threshold.
- [ ] incident closes on recovery.
- [ ] recovery duration is correct.
- [ ] flapping detected and notification spam suppressed.
- [ ] maintenance suppresses configured alerts.
- [ ] raw and adjusted uptime differ correctly.
- [ ] dependency suppresses redundant child alerts.
- [ ] incident summaries retained indefinitely.

## History
- [ ] raw history retained 7 days.
- [ ] 5m rollups cover 7–30 days.
- [ ] hourly rollups cover 30–365 days.
- [ ] daily rollups cover >365 days.
- [ ] deletion occurs only after successful rollup.
- [ ] current/min/avg/max/p95 shown appropriately.
- [ ] graph ranges include presets + custom range.

## Notifications
- [ ] SMTP works.
- [ ] Telegram works.
- [ ] Discord works.
- [ ] webhook works.
- [ ] reusable profiles work.
- [ ] severity routing works.
- [ ] quiet hours work.
- [ ] critical bypass behavior documented.
- [ ] unresolved reminder sends at most configured one reminder.
- [ ] failed delivery retries are bounded.
- [ ] channel health shown.
- [ ] test notification works.
- [ ] simulated incident works.

## Status pages
- [ ] multiple pages.
- [ ] public mode.
- [ ] authenticated mode.
- [ ] password mode.
- [ ] unlisted mode.
- [ ] custom public display name.
- [ ] groups.
- [ ] independent theme.
- [ ] accent/logo/title/description.
- [ ] custom hostname mapping.
- [ ] default 30d incidents.
- [ ] manual note.
- [ ] JSON endpoint.
- [ ] feed.
- [ ] Powered by Sinjal subtle and removable.
- [ ] no internal target leakage by default.

## UI
- [ ] sidebar nav.
- [ ] problem-first overview.
- [ ] compact monitor rows.
- [ ] detail tabs.
- [ ] progressive create/edit form.
- [ ] command palette.
- [ ] shortcuts.
- [ ] compact/comfortable density.
- [ ] mobile monitoring experience is good.
- [ ] Carbon complete.
- [ ] Paper complete.
- [ ] Midnight complete.
- [ ] Terminal complete.
- [ ] no generic stock-dashboard appearance.

## Accessibility
- [ ] keyboard navigation.
- [ ] visible focus.
- [ ] status not color-only.
- [ ] reduced motion.
- [ ] contrast validated for all themes.
- [ ] form errors accessible.
- [ ] charts have textual summary.

## Backups/migrations
- [ ] daily local backup.
- [ ] 14-day default retention.
- [ ] manual backup.
- [ ] restore.
- [ ] safe config export.
- [ ] full backup.
- [ ] pre-migration backup mandatory.
- [ ] downgrade documented through backup restore.
- [ ] DB integrity command.

## Reliability
- [ ] SQLite busy bounded retry.
- [ ] DB corruption degrades safely.
- [ ] disk warning thresholds.
- [ ] network calls bounded by context.
- [ ] huge body cap.
- [ ] notification provider outage bounded.
- [ ] internet/root dependency behavior supported.
- [ ] clock/DST does not corrupt interval scheduling.

## Performance
- [ ] idle RAM target evaluated.
- [ ] startup target evaluated.
- [ ] JS payload measured.
- [ ] container size measured.
- [ ] 100 monitors trivial.
- [ ] 1,000 monitor benchmark completed.
- [ ] no idle goroutine growth proportional to monitor count.

## Release/repository
- [ ] MIT license.
- [ ] Issues templates.
- [ ] PR-not-accepted policy.
- [ ] AI PR policy.
- [ ] manual release build script.
- [ ] no automatic publishing.
- [ ] checksums.
- [ ] SBOM.
- [ ] signatures.
