# Manual Release Procedure

Sinjal releases are deliberately manual. The release model, artifacts and signing are specified in `17_MIGRATIONS_RELEASES.md`.

## Preconditions

- clean git tree
- tests passing
- benchmarks reviewed when relevant
- changelog updated
- migration notes reviewed
- version chosen

## Build

Run the local helper (M11-03; today only the draft `scripts/release.sh.example` exists):

```bash
./scripts/release.sh 0.3.0
```

The helper may:

- run tests
- build linux/amd64
- build linux/arm64
- generate checksums
- generate the SBOM (syft, SPDX JSON)
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
- that the signature verifies against `docs/release-signing/allowed_signers`
- version output
- clean install
- upgrade from previous version
- backup/restore

## Publish manually

The owner manually:

- creates the git tag
- pushes the tag
- creates the GitHub release
- uploads artifacts
- builds/pushes the Docker image if desired

## Continuous integration

A check-only GitHub Actions workflow is allowed (decision P0-18). It never publishes.

Triggers:

- push to `master`
- manual `workflow_dispatch`

Workflow: `.github/workflows/ci.yml`, one job with two check steps after checkout and Go setup:

- `make lint`: `gofmt` check, `go vet ./...`, `staticcheck`, `templ fmt` check, stale generated templ output check (`go tool templ generate` must change nothing)
- `make test-race`: `go test -race ./...`, which includes the milestone integration tests

Constraints:

- `permissions: contents: read`
- third-party actions pinned by full commit SHA
- no repository secrets
- no artifact uploads, releases, tags or container images
- the browser suite (`make test-browser`) and manual notification tests do not run in CI

CI is a convenience signal. The quality gates in `39_QUALITY_GATES.md` are still run locally before a milestone or release is accepted.

## Rollback note

If a release includes a migration, its release notes must remind users that a downgrade requires restoring the pre-upgrade backup.
