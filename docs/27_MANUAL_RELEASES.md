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
- generate SBOM (syft, SPDX JSON)
- sign `checksums.txt` with the SSH release key if `SINJAL_RELEASE_KEY` is set (see `17_MIGRATIONS_RELEASES.md`)

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
- signature verifies against `docs/release-signing/allowed_signers`
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

## Continuous integration

A check-only GitHub Actions workflow is allowed (decision P0-18). It never publishes.

Triggers:
- push to `master`
- manual `workflow_dispatch`

Steps:
- `gofmt` check (no diff)
- `go vet ./...`
- `make lint` (staticcheck, `templ fmt` check)
- `go test -race ./...`
- stale generated templ output check (`go tool templ generate` produces no diff)

Constraints:
- `permissions: contents: read`
- third-party actions pinned by full commit SHA
- no repository secrets
- no artifact uploads, releases, tags or container images
- the browser suite (`make test-browser`) and manual notification tests do not run in CI

CI is a convenience signal. The quality gates in `39_QUALITY_GATES.md` are still run locally before a milestone or release is accepted.

## Rollback note
If migration occurs, release notes must remind users that downgrade requires restoring the pre-upgrade backup.
