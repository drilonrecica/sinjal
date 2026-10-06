# Migrations, Versioning and Releases

## Versioning

Semantic Versioning.

Before 1.0:
- breaking API/config changes permitted
- database upgrade safety still required

## Migration system

Embedded numbered SQL migrations:

```text
migrations/
  001_init.sql
  002_status_pages.sql
  003_notification_profiles.sql
```

Rules:
- append-only
- never edit an already-released migration
- schema version recorded in DB
- migration runs at startup
- pre-migration backup mandatory

## Downgrade

No reverse migrations.

Downgrade procedure:
1. stop new version
2. restore pre-upgrade backup
3. run previous binary/container

## Release model

**Manual builds and manual uploads.**

No tag-triggered publishing.
No automatic GitHub release publication.
No automatic Docker registry publication.

A local helper script may perform deterministic build chores, but the owner explicitly decides when and where to upload artifacts.

## Manual release helper

Target:

```bash
./scripts/release.sh 0.3.0
```

Output:

```text
dist/
  sinjal-linux-amd64
  sinjal-linux-arm64
  checksums.txt
  sbom.spdx.json
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

## Change log

Maintain `CHANGELOG.md` once real releases begin.

Release notes:
- features
- fixes
- migrations
- breaking changes
- rollback notes if relevant
