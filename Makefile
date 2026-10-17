BINARY := mcp-db-server
PKG    := ./cmd/$(BINARY)

# Load local env for `make run` (never committed).
-include .env
export

.PHONY: build test lint run up down tidy clean

build:
	go build -o bin/$(BINARY) $(PKG)

test:
	go test -race -count=1 ./...

lint:
	gofmt -l . | (! grep .) || (echo "gofmt needed" && exit 1)
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "golangci-lint not installed, skipping"; fi

run:
	go run $(PKG)

up:
	docker compose up -d

down:
	docker compose down

tidy:
	go mod tidy

clean:
	rm -rf bin
