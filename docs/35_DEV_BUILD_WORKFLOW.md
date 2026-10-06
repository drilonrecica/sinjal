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

Generated Go files:
- generation command documented
- CI/local check should detect stale generated output if committed

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

At minimum:
- `gofmt`
- `go vet`
- static analysis/linter selected conservatively
- templ formatting if applicable

Avoid enormous lint configurations that dominate development.

## Reproducibility

`go.mod` and `go.sum` are authoritative.

Vendoring is optional, not required by product design.
