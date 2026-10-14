# Development and Build Workflow

## Development goals

A contributor/owner should be able to run Sinjal with:

- Go
- the templ generation tool as needed
- any static asset build step chosen by the implementation

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

Built: the `Makefile` has these targets:

| Target | What it does |
|---|---|
| `dev` | `go run ./cmd/sinjal serve` with `SINJAL_DATA_DIR=./.dev-data` |
| `generate` | `go tool templ generate` |
| `fmt` | `gofmt -w .` and `go tool templ fmt .` |
| `lint` | static checks, see [Formatting/linting](#formattinglinting) |
| `check-templ-fmt`, `check-generated` | the two templ steps of `lint` |
| `test` | `go test ./...` |
| `test-race` | `go test -race ./...` |
| `test-browser` | the chromedp suite, see [Browser tests](#browser-tests) |
| `bench` | `go test -run='^$' -bench=. -benchmem ./...` |
| `build` | `CGO_ENABLED=0 go build -trimpath` with `-s -w` and `-X main.version` from `git describe`, output `./sinjal` |
| `reset-dev-db` | deletes `./.dev-data` |
| `release-local` | not implemented until M11; fails |

## Templ

Generated `*_templ.go` files are committed, so a plain `go build` works without the generator.

- templ sources live in `web/templates/`
- `make generate` regenerates them (`go tool templ generate`; the generator is pinned by the `tool` directive in `go.mod`)
- `make check-generated` (part of `make lint`) regenerates and fails if any `*_templ.go` changed, which means the committed output was stale. It compares file hashes, so it works on a dirty tree and in CI.

## Frontend assets

If a Node-based tool is used during development for CSS/minification:

- pin it
- keep it dev-only
- the final binary embeds built assets
- the production runtime remains Go only

Prefer avoiding Node entirely if cleanly possible. Built: no Node tool is used; CSS and JS are hand-written or vendored files under `web/static`, embedded as they are.

## Database dev

Use a temporary data directory in development. Built: `make dev` uses `./.dev-data`.

Provide:

- a reset dev DB command (built: `make reset-dev-db`)
- optional demo monitor seeding, only in explicit development mode

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

templ has no check-only mode, so steps 4 and 5 run the tool and fail when it changed any file. The files are then already fixed and only need committing.

No golangci-lint and no large lint configuration.

## Browser tests

`make test-browser` runs the chromedp suite in `tests/browser` (its own module, `20_TESTING.md` "UI/browser tests").

- It builds the binary once per run and needs a local Chrome or Chromium; without one, the tests skip.
- It takes about half a minute.
- It is not part of `make test`, `make lint` or CI. `gofmt` covers its files; `go vet ./...` at the root does not.

## Reproducibility

`go.mod` and `go.sum` are authoritative.

Vendoring is optional, not required by product design.
