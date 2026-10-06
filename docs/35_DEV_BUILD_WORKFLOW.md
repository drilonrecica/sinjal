# Development and Build Workflow

## Development goals

A contributor/owner should be able to run Sinjal with:
- Go
- the templ generation tool as needed
- any static asset build step chosen by implementation

Production must not require Node.

## Suggested commands

Provide a Makefile or small task script with targets similar to:

```text
dev
generate
test
test-race
bench
lint
build
release-local
```

Do not add a large task runner dependency.

## Templ

Generated `*_templ.go` files are committed, so a plain `go build` works without the generator.

- `make generate` regenerates them (`go tool templ generate`; the generator is pinned by the `tool` directive in `go.mod`)
- `make check-generated` (part of `make lint`) regenerates and fails if any `*_templ.go` changed, which means the committed output was stale; it compares file hashes, so it works on a dirty tree and in CI
- templ sources live in `web/templates/`

## Frontend assets

If a Node-based tool is used during development for CSS/minification:
- pin it
- keep it dev-only
- final binary embeds built assets
- production runtime remains Go only

Prefer avoiding Node entirely if cleanly possible.

## Database dev

Use temporary `/data` in development.

Provide:
- reset dev DB command
- seed optional demo monitors only in explicit development mode

Never seed production automatically.

## Test services

Use local Go test servers/listeners rather than external public dependencies.

## Formatting/linting

`make fmt` formats Go (`gofmt`) and templ (`templ fmt`) sources.

`make lint` runs, in order:
1. `gofmt -l` (must list nothing)
2. `go vet ./...`
3. `staticcheck ./...` with its default checks (pinned with the `tool` directive in `go.mod`, run as `go tool staticcheck`; dev-only)
4. templ formatting (`make check-templ-fmt`)
5. stale generated templ output (`make check-generated`)

Steps 4 and 5 have no check-only mode in templ, so they run the tool and fail when it changed any file; the files are then already fixed and only need committing.

No golangci-lint and no large lint configuration.

## Reproducibility

`go.mod` and `go.sum` are authoritative.

Vendoring is optional, not required by product design.
