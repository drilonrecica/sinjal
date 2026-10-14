# Quality Gates

A milestone or release candidate is not done until its relevant gates pass.

The check-only CI workflow (`27_MANUAL_RELEASES.md` "Continuous integration") runs `make lint` and `make test-race` on push, which covers most of the backend gate. Local runs remain authoritative.

## Backend gate

- gofmt clean
- go vet clean
- staticcheck clean
- templ formatting and generated output current (`make lint` covers all of the above)
- tests pass (`make test`; `make test-race` for the race check below)
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

- all three themes checked, Stark with Ambient light on and off
- comfortable + compact checked
- mobile width checked
- keyboard flow checked
- reduced motion checked
- empty/error/loading states exist

## Performance gate

- JS budget checked (`18_PERFORMANCE.md` "Budgets")
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
