# Migrations, Versioning and Releases

## Versioning

Semantic Versioning.

Before 1.0:

- breaking API/config changes are permitted
- database upgrade safety is still required

## Migration system

Embedded numbered SQL migrations:

```text
internal/db/migrations/
  001_foundation.sql      # M0: system_settings
  002_auth.sql            # M1: users (+ UI prefs), sessions, passkeys, audit_events
  003_monitors.sql        # M2: monitors, monitor_pauses, http config, secrets, tags, check_results,
                          #     minimal notification_profiles (FK target)
  004_incidents.sql       # M3: incidents (+ notification state), incident_events,
                          #     maintenance_windows; opens an incident for every
                          #     monitor that is DOWN at upgrade time
  005_monitor_types.sql   # M4: tcp/icmp/dns/heartbeat config
  006_notifications.sql   # M5: channels, full profiles, routes, deliveries, tls_warnings
  007_aggregates.sql      # M6: check_aggregates
  008_status_pages.sql    # M7: pages, groups, page monitors, hosts
  009_themes.sql          # M8: Clear themes, Ambient light
  010_saved_views.sql     # M8: saved_views
```

One migration per milestone, created with the milestone that first needs the tables. `spec/schema.sql` is the consolidated end-state reference; the migrations are authoritative.

Rules:

- append-only
- never edit an already-released migration
- schema version recorded in the DB
- migrations run at startup
- pre-migration backup mandatory

Runner behaviour (built, `internal/db/migrate.go`):

- Migration files are `NNN_name.sql`, numbered contiguously from 001. Each runs in its own transaction and is recorded in `schema_migrations`.
- Before applying anything to a database that already has tables, the runner writes `<data dir>/backups/pre-migration-<app version>-<UTC timestamp>.db` with `VACUUM INTO`. If that fails, nothing is migrated and startup aborts.
- The only exception is a brand-new empty database, which has nothing to back up.
- A database whose recorded version is newer than the binary knows is refused untouched (downgrade = restore the pre-upgrade backup).
- A failed migration rolls back its own transaction; earlier migrations in the same run stay applied.

## Downgrade

No reverse migrations.

Downgrade procedure:

1. Stop the new version.
2. Restore the pre-upgrade backup.
3. Run the previous binary/container.

## Release model

**Manual builds and manual uploads.**

- No tag-triggered publishing.
- No automatic GitHub release publication.
- No automatic Docker registry publication.

A local helper script may perform deterministic build chores, but the owner explicitly decides when and where to upload artifacts. The step-by-step procedure is `27_MANUAL_RELEASES.md`.

## Manual release helper

Target (M11-03):

```bash
./scripts/release.sh 0.3.0
```

Today only the draft `scripts/release.sh.example` exists, and `make release-local` fails with "not implemented until M11".

Output:

```text
dist/
  sinjal-linux-amd64
  sinjal-linux-arm64
  checksums.txt
  sbom.spdx.json
  syft-version.txt
  signatures/
```

The script must not publish anything.

## Official architectures

- linux/amd64
- linux/arm64

Other builds may work but are not guaranteed in v1.

## Supply-chain artifacts

Target:

- checksums
- SBOM
- signatures

Do not add runtime complexity for release tooling.

Tooling (decision P0-17):

- checksums: `sha256sum` over the binaries and the SBOM → `checksums.txt`
- SBOM: `syft scan file:dist/sinjal-linux-amd64 -o spdx-json=dist/sbom.spdx.json` (dev-only tool; the `syft version` output is saved next to the artifacts as `syft-version.txt`)
- signature: `ssh-keygen -Y sign -n sinjal-release` over `checksums.txt` → `signatures/checksums.txt.sig`
- signing key: a **dedicated Ed25519 release key**, not a login key; generated and stored privately by the owner; passed to the release script via `SINJAL_RELEASE_KEY`
- public key: `docs/release-signing/allowed_signers`

Verifying a release:

```bash
sha256sum -c checksums.txt
ssh-keygen -Y verify -f allowed_signers -I release@sinjal -n sinjal-release \
  -s checksums.txt.sig < checksums.txt
```

No external signing service, transparency log or keyless OIDC flow.

## Change log

Maintain `CHANGELOG.md` once real releases begin.

Release notes:

- features
- fixes
- migrations
- breaking changes
- rollback notes if relevant
