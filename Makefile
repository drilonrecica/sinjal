VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_DATA := ./.dev-data

.PHONY: dev generate fmt lint test test-race bench build reset-dev-db release-local

dev:
	SINJAL_DATA_DIR=$(DEV_DATA) go run ./cmd/sinjal serve

generate:
	go tool templ generate

fmt:
	gofmt -w .
	go tool templ fmt .

# staticcheck and the stale-templ check are added in M0-16 / M0-12.
lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...

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
