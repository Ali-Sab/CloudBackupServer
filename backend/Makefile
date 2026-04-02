.PHONY: build run test test-integration deps lint clean help

BIN := bin/server

## build: compile the server binary
build:
	@mkdir -p bin
	go build -ldflags="-s -w" -o $(BIN) ./cmd/server

## run: run the server locally (requires DATABASE_URL and JWT_SECRET env vars)
run:
	go run ./cmd/server

## test: run unit tests (no database required)
test:
	go test -v -race ./...

## test-integration: run integration tests (requires TEST_DATABASE_URL env var)
test-integration:
	go test -v -race -tags integration ./...

## deps: download and tidy Go modules
deps:
	go mod tidy
	go mod verify

## lint: run golangci-lint (must be installed separately)
lint:
	golangci-lint run ./...

## clean: remove build artefacts
clean:
	rm -rf bin/

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/## //'
