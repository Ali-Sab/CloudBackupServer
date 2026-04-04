# Development Guide

## Prerequisites

See [README.md](../README.md#prerequisites) for the full tool list.

---

## First-time setup

```bash
git clone https://github.com/ali-sab/cloudbackupserver.git
cd cloudbackupserver

make setup
# → copies .env.example to .env
# → runs go mod tidy (generates go.sum)
# → runs npm install in frontend/

# Edit .env — at minimum set a real JWT_SECRET:
openssl rand -hex 32   # copy the output into JWT_SECRET in .env
```

---

## Starting services

```bash
make up       # starts postgres + backend in Docker
make ps       # verify both are "healthy" / "running"
make logs     # follow combined logs
```

### Colima (macOS)

```bash
colima start --cpu 2 --memory 4   # first time, or after a reboot
make up
```

---

## Running the frontend

```bash
cd frontend
npm start     # opens an Electron window pointing at http://localhost:8080
```

The window shows the session state. Use the Sign In / Create Account forms to authenticate.

For a live-reload workflow, consider [electron-reload](https://github.com/yan-foto/electron-reload):

```bash
npm install --save-dev electron-reload
```

Then add to `src/main.js`:
```js
if (process.env.NODE_ENV === 'development') {
  require('electron-reload')(__dirname);
}
```

---

## Running tests

### Backend unit tests (no database)

```bash
make test-backend
# or
cd backend && go test -v -race ./...
```

### Frontend tests

```bash
make test-frontend
# or
cd frontend && npm test
```

### Backend integration tests

Integration tests exercise the full HTTP stack against a real PostgreSQL database.

```bash
# Using the compose database
export TEST_DATABASE_URL="postgres://cloudbackup:cloudbackup_dev@localhost:5432/cloudbackup?sslmode=disable"
make test-integration
```

Tests skip automatically if `TEST_DATABASE_URL` is not set.

---

## Adding a migration

```bash
# 1 — Create the SQL file (use the next sequential number)
cat > backend/migrations/00002_add_files_table.sql <<'SQL'
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS files;
-- +goose StatementEnd
SQL

# 2 — Rebuild the backend image so the migration is embedded
make build

# 3 — Restart the backend — migration applies on startup
make down && make up
```

---

## Rebuilding after code changes

```bash
make build      # rebuilds Docker images
make down       # stop running containers
make up         # restart with new images
```

For fast iteration on the backend without Docker:

```bash
export DATABASE_URL="postgres://cloudbackup:cloudbackup_dev@localhost:5432/cloudbackup?sslmode=disable"
export JWT_SECRET="dev-secret"
cd backend && go run ./cmd/server
```

---

## Project structure quick reference

```
backend/cmd/server/main.go       Entry point
backend/internal/api/handlers.go HTTP handlers (add new endpoints here)
backend/internal/api/router.go   Route registration
backend/internal/db/db.go        Database helpers
backend/internal/session/        JWT logic
backend/migrations/              SQL migration files
backend/api/openapi.yaml         OpenAPI spec (keep in sync with handlers)

frontend/src/renderer/app.js     Frontend logic (CloudBackup module)
frontend/src/main.js             Electron main process
frontend/__tests__/              Jest tests
```

---

## Code style

- **Go**: follow standard `gofmt` formatting; run `golangci-lint` before committing.
- **JavaScript**: no bundler — vanilla ES2020; keep the `CloudBackup` module pure/testable.
- **SQL**: snake_case column names; add `NOT NULL` and `DEFAULT` where appropriate.
- **Commits**: use conventional-commit style (`feat:`, `fix:`, `docs:`, `chore:`).
