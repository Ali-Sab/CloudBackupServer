# Cloud Backup Server

A monorepo containing the Cloud Backup service: a Go REST backend, an Electron desktop client, and a PostgreSQL database — all runnable locally with Docker Compose (or Colima on macOS).

---

## Repository layout

```
CloudBackupServer/
├── backend/          Go REST API (chi, pgx, goose, JWT)
├── frontend/         Electron desktop client
├── docs/             Architecture, development, and API guides
├── docker-compose.yml
├── .env.example
└── Makefile          Top-level convenience targets
```

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | ≥ 1.21 | [go.dev/dl](https://go.dev/dl/) |
| Node.js | ≥ 18 | For Electron frontend |
| Docker + Compose | v2 | Bundled with Docker Desktop |
| **Colima** *(macOS only)* | latest | See below |

### Colima (macOS alternative to Docker Desktop)

```bash
brew install colima docker docker-compose
colima start --cpu 2 --memory 4
```

After that, `docker compose` works exactly as it does on Linux.

---

## Quick start

```bash
# 1 — Clone and install deps
git clone https://github.com/ali-sab/cloudbackupserver.git
cd cloudbackupserver
make setup          # copies .env.example → .env, runs go mod tidy, npm install

# 2 — Edit .env: set a real JWT_SECRET (and any other values you want to change)
$EDITOR .env

# 3 — Start services
make up             # starts postgres + backend via docker compose

# 4 — Verify
curl http://localhost:8080/api/health
# → {"status":"ok","version":"0.1.0"}

curl http://localhost:8080/api/session
# → {"logged_in":false}

# 5 — Run the desktop frontend (in a separate terminal)
cd frontend && npm start
```

---

## API

The full OpenAPI 3.0 specification lives at [`backend/api/openapi.yaml`](backend/api/openapi.yaml).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/api/health` | — | Health check |
| `GET` | `/api/session` | Optional JWT | Current session state |
| `POST` | `/api/auth/register` | — | Register a new user |
| `POST` | `/api/auth/login` | — | Log in, receive JWT |

Authentication uses **Bearer tokens** (JWT, 24 h TTL).  
Pass the token as: `Authorization: Bearer <token>`

### Quick examples

```bash
# Register
curl -s -X POST http://localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","email":"alice@example.com","password":"hunter2"}' | jq .

# Login
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"hunter2"}' | jq -r .token)

# Authenticated session check
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/session | jq .
```

---

## Database migrations

Migrations are embedded in the backend binary and run automatically at startup using [goose](https://github.com/pressly/goose).

Migration files live in [`backend/migrations/`](backend/migrations/).  
To add a new migration:

```bash
# Create the next migration file
cat > backend/migrations/00002_add_files_table.sql <<'SQL'
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id),
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
```

The next time the backend starts (or `make up` is run), the migration will be applied.

---

## Testing

```bash
# Unit tests — no database needed
make test-backend      # Go unit tests
make test-frontend     # Jest tests

# Both at once
make test

# Integration tests — requires a running PostgreSQL instance
export TEST_DATABASE_URL="postgres://cloudbackup:cloudbackup_dev@localhost:5432/cloudbackup?sslmode=disable"
make test-integration
```

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | *(required)* | PostgreSQL connection string |
| `JWT_SECRET` | *(required)* | JWT signing secret — use a long random value |
| `PORT` | `8080` | Port the backend listens on |
| `POSTGRES_PASSWORD` | `cloudbackup_dev` | Postgres password (docker compose) |
| `BACKEND_PORT` | `8080` | Exposed backend port (docker compose) |
| `TEST_DATABASE_URL` | *(empty)* | Database for integration tests; tests skip if unset |

Generate a secure JWT secret:

```bash
openssl rand -hex 32
```

---

## Useful make targets

```
make setup             Install all deps, create .env
make up                Start services (docker compose)
make down              Stop services
make ps                Show service status
make logs              Follow logs
make build             Rebuild Docker images
make test              Run all tests
make test-backend      Go unit tests
make test-frontend     Jest tests
make test-integration  Integration tests (needs TEST_DATABASE_URL)
make clean             Remove build artefacts
```

---

## Documentation

| Doc | Description |
|-----|-------------|
| [docs/architecture.md](docs/architecture.md) | System design and component overview |
| [docs/development.md](docs/development.md) | Local development workflow |
| [docs/api.md](docs/api.md) | API reference and usage patterns |
| [backend/api/openapi.yaml](backend/api/openapi.yaml) | Machine-readable OpenAPI 3.0 spec |

---

## License

[GNU General Public License v3.0](LICENSE)
