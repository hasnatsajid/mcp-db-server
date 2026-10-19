BINARY  := mcp-db-server
PKG     := ./cmd/$(BINARY)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Load local env for `make run` (never committed).
-include .env
export

.PHONY: build test test-integration fuzz lint run up down tidy clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) $(PKG)

test:
	go test -race -count=1 ./...

# Needs MCP_TEST_DATABASE_URL (see .env.example), e.g. against `make up`.
test-integration:
	go test -race -count=1 -tags integration ./...

fuzz:
	go test -run='^$$' -fuzz=FuzzValidateSelect -fuzztime=60s ./internal/guard

lint:
	gofmt -l . | (! grep .) || (echo "gofmt needed" && exit 1)
	go vet ./...
	go vet -tags integration ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run --build-tags integration ./...; else echo "golangci-lint not installed, skipping"; fi

run:
	go run $(PKG)

up:
	docker compose up -d --wait

down:
	docker compose down

tidy:
	go mod tidy

clean:
	rm -rf bin
