VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_DATA := ./.dev-data

.PHONY: dev generate check-generated check-templ-fmt fmt lint test test-race bench build reset-dev-db release-local

dev:
	SINJAL_DATA_DIR=$(DEV_DATA) go run ./cmd/sinjal serve

generate:
	go tool templ generate

fmt:
	gofmt -w .
	go tool templ fmt .

# Fails when committed *_templ.go files do not match their .templ sources.
# Hash-based rather than `git diff`, so it also works on a dirty tree and in CI.
check-generated:
	@sum() { find . -name '*_templ.go' -not -path './.git/*' | LC_ALL=C sort | xargs -r sha256sum; }; \
	before=$$(sum); go tool templ generate >/dev/null 2>&1; after=$$(sum); \
	if [ "$$before" != "$$after" ]; then \
	  echo "generated templ files were stale and have been regenerated: commit them" >&2; exit 1; fi

# templ fmt has no check-only mode, so compare file hashes around a format run.
check-templ-fmt:
	@sum() { find . -name '*.templ' -not -path './.git/*' | LC_ALL=C sort | xargs -r sha256sum; }; \
	before=$$(sum); go tool templ fmt . >/dev/null 2>&1; after=$$(sum); \
	if [ "$$before" != "$$after" ]; then \
	  echo "templ files were not formatted and have been reformatted: commit them (make fmt)" >&2; exit 1; fi

# Static checks, in order: gofmt, go vet, staticcheck, templ formatting,
# stale generated templ output. Run sequentially because the last two rewrite files.
lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...
	go tool staticcheck ./...
	@$(MAKE) --no-print-directory check-templ-fmt
	@$(MAKE) --no-print-directory check-generated

test:
	go test ./...

test-race:
	go test -race ./...

bench:
	go test -run='^$$' -bench=. -benchmem ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o sinjal ./cmd/sinjal

reset-dev-db:
	rm -rf $(DEV_DATA)

release-local:
	@echo "release-local is not implemented until M11" >&2; exit 1
