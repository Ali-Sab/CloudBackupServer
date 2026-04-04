.PHONY: setup up down ps logs build \
        test test-backend test-frontend test-integration \
        clean help

# ---- Setup ---------------------------------------------------------------

## setup: install all dependencies (Go modules + npm packages)
setup:
	@echo "==> Setting up .env"
	@test -f .env || (cp .env.example .env && echo "    Created .env from .env.example — edit it before continuing")
	@echo "==> Installing Go dependencies"
	cd backend && go mod tidy
	@echo "==> Installing frontend dependencies"
	cd frontend && npm install
	@echo ""
	@echo "Done! Next steps:"
	@echo "  1. Edit .env (set a real JWT_SECRET)"
	@echo "  2. Run 'make up' to start the services"

# ---- Docker Compose -------------------------------------------------------

## up: start postgres + backend with docker compose
up:
	docker compose up -d
	@echo "Backend → http://localhost:$$(grep BACKEND_PORT .env 2>/dev/null | cut -d= -f2 || echo 8080)"

## down: stop all services
down:
	docker compose down

## ps: show running service status
ps:
	docker compose ps

## logs: follow logs for all services (Ctrl-C to exit)
logs:
	docker compose logs -f

## build: rebuild all Docker images
build:
	docker compose build

# ---- Testing --------------------------------------------------------------

## test: run all tests (backend unit + frontend)
test: test-backend test-frontend

## test-backend: run Go unit tests (no database required)
test-backend:
	cd backend && go test -v -race ./...

## test-frontend: run Jest tests
test-frontend:
	cd frontend && npm test -- --ci

## test-integration: run Go integration tests (requires TEST_DATABASE_URL)
test-integration:
	@test -n "$(TEST_DATABASE_URL)" || (echo "Error: TEST_DATABASE_URL is not set" && exit 1)
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) cd backend && go test -v -race -tags integration ./...

# ---- Misc ----------------------------------------------------------------

## clean: remove local build artefacts and node_modules
clean:
	cd backend && rm -rf bin/
	cd frontend && rm -rf node_modules/ dist/

## help: list available make targets
help:
	@grep -E '^## ' Makefile | sed 's/## //'
