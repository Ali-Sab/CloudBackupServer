.PHONY: setup up down db-reset ps logs build \
        test test-backend test-frontend test-integration test-e2e \
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

## up: rebuild backend image and start all services
up:
	docker compose up -d --build
	@echo "Backend → http://localhost:$$(grep BACKEND_PORT .env 2>/dev/null | cut -d= -f2 || echo 8080)"

## down: stop all services
down:
	docker compose down

## db-reset: wipe the database volume and restart fresh
db-reset:
	docker compose down
	docker volume rm cloudbackupserver_postgres_data
	docker compose up -d --build

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

## test-integration: start postgres if needed, then run Go integration tests
test-integration:
	@echo "==> Ensuring postgres is running"
	docker compose up -d postgres
	@echo "==> Waiting for postgres to be healthy"
	@until docker compose exec postgres pg_isready -U cloudbackup -d cloudbackup > /dev/null 2>&1; do sleep 1; done
	@echo "==> Running integration tests"
	@set -a && . ./.env && set +a && \
	cd backend && TEST_DATABASE_URL="postgres://cloudbackup:$${POSTGRES_PASSWORD:-cloudbackup_dev}@localhost:$${POSTGRES_PORT:-5432}/cloudbackup?sslmode=disable" go test -v -race -tags integration ./...

## test-e2e: run Electron smoke tests (requires: make up)
test-e2e:
	@echo "==> Running Electron E2E smoke tests (backend must be running)"
	cd frontend && npm run test:e2e

# ---- Misc ----------------------------------------------------------------

## clean: remove local build artefacts and node_modules
clean:
	cd backend && rm -rf bin/
	cd frontend && rm -rf node_modules/ dist/

## help: list available make targets
help:
	@grep -E '^## ' Makefile | sed 's/## //'
