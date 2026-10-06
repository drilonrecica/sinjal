# Manual Release Procedure

Sinjal releases are deliberately manual.

## Preconditions
- clean git tree
- tests passing
- benchmarks reviewed when relevant
- changelog updated
- migration notes reviewed
- version chosen

## Build

Run local helper:

```bash
./scripts/release.sh 0.3.0
```

The helper may:
- run tests
- build linux/amd64
- build linux/arm64
- generate checksums
- generate SBOM
- sign artifacts if signing configuration exists

It must NOT:
- push git tags
- create GitHub releases
- upload artifacts
- push container images

## Owner review
Inspect:
- artifact sizes
- checksum file
- SBOM
- version output
- clean install
- upgrade from previous version
- backup/restore

## Publish manually
Owner manually:
- creates git tag
- pushes tag
- creates GitHub release
- uploads artifacts
- builds/pushes Docker image if desired

## Rollback note
If migration occurs, release notes must remind users that downgrade requires restoring the pre-upgrade backup.
