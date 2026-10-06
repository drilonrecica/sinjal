# Quality Gates

A milestone or release candidate is not done until its relevant gates pass.

The backend gate is also run by the check-only CI workflow on push (`27_MANUAL_RELEASES.md`), but local runs remain authoritative.

## Backend gate
- gofmt clean
- go vet clean
- tests pass
- no race found in exercised concurrent areas
- DB migration from prior fixture works
- no new unbounded queue/goroutine behavior

## Security gate
- no secret logging
- authz test for viewer/admin boundaries
- CSRF covered
- status-page redaction covered
- backup extraction safe
- trusted proxy tests

## UI gate
- all four themes checked
- comfortable + compact checked
- mobile width checked
- keyboard flow checked
- reduced motion checked
- empty/error/loading states exist

## Performance gate
- JS budget checked
- image/container size checked
- idle memory sampled
- relevant benchmark compared
- 1,000 monitor scenario before v1

## Operations gate
- clean install
- restart with active incident
- backup
- restore
- migration
- graceful shutdown

## Documentation gate
- behavior docs updated
- config examples updated
- API spec updated if applicable
- changelog updated for release
