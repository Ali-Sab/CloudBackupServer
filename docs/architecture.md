# Architecture

## Overview

Cloud Backup is structured as a monorepo with two deployable components:

```
┌─────────────────────────────────────────────────────────┐
│                       Developer host                     │
│                                                          │
│  ┌───────────────────────┐                              │
│  │  Electron frontend    │  npm start / Electron        │
│  │  (renderer process)   │──── fetch ──────────────┐    │
│  │  src/renderer/app.js  │                         │    │
│  └───────────────────────┘                         │    │
│                                              HTTP :8080  │
│  ┌─────────────────── Docker Compose ────────────────┐  │
│  │                                              │    │  │
│  │  ┌──────────────────────┐         ┌──────────▼──┐ │  │
│  │  │  PostgreSQL :5432    │◄── SQL ─│  Go backend │ │  │
│  │  │  postgres:16-alpine  │         │  :8080      │ │  │
│  │  └──────────────────────┘         └─────────────┘ │  │
│  └────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

The Electron desktop client runs natively on the host and communicates with the backend over HTTP. The backend and database run in Docker (or Colima on macOS).

---

## Backend (Go)

### Package structure

```
backend/
├── cmd/server/main.go          Entry point: env, migrations, wiring, HTTP server
├── internal/
│   ├── api/
│   │   ├── router.go           Route definitions (chi), middleware registration
│   │   ├── handlers.go         HTTP handler methods + request/response types
│   │   └── middleware.go       CORS
│   ├── db/
│   │   └── db.go               DB pool creation, migrations runner, CRUD helpers
│   ├── models/
│   │   └── user.go             User domain model
│   └── session/
│       └── session.go          JWT create / validate
├── migrations/
│   ├── embed.go                embed.FS declaration
│   └── 00001_create_users.sql  First migration (goose format)
└── api/
    └── openapi.yaml            OpenAPI 3.0 contract
```

### Request lifecycle

```
HTTP request
  → chi router (RequestID, RealIP, Logger, Recoverer, CORS)
  → handler method (Handler struct)
      → session.ValidateToken  (session endpoints)
      → db.GetUserByUsername   (auth endpoints)
      → bcrypt.CompareHashAndPassword
  → JSON response
```

### Authentication

- Stateless JWT (HS256, 24 h TTL)
- Token returned on `/api/auth/login` and `/api/auth/register`
- Validated on `/api/session` — no database lookup needed
- Client stores token in `localStorage` (renderer) and Electron main process (via IPC)

### Database migrations

Managed by [goose](https://github.com/pressly/goose) with SQL files embedded in the binary via Go's `embed` package. Migrations run idempotently at every startup, so no separate migration step is needed in production.

---

## Frontend (Electron)

```
frontend/
├── src/
│   ├── main.js        Electron main process — creates BrowserWindow, IPC handlers
│   ├── preload.js     contextBridge — exposes window.electronAPI safely
│   └── renderer/
│       ├── index.html HTML shell
│       ├── app.js     Business logic (CloudBackup module, module.exports for Jest)
│       └── styles.css Dark-mode UI
└── __tests__/
    └── session.test.js Jest unit tests (jsdom)
```

### Security model

| Concern | Approach |
|---------|----------|
| Node access in renderer | Disabled (`nodeIntegration: false`) |
| Renderer ↔ main isolation | `contextIsolation: true` + `contextBridge` |
| XSS | `escapeHtml()` for all dynamic content + strict CSP header |
| CORS | Backend allows `*` origin for local development |

### Token storage

```
Renderer (app.js)
  ├── window.electronAPI.getToken()  ← ipcRenderer.sendSync('get-token')
  └── window.electronAPI.setToken()  ← ipcRenderer.sendSync('set-token', token)
           ↕ IPC
Main process (main.js)
  └── let authToken = null   (in-memory, process lifetime)
```

`localStorage` is also used as a fallback for non-Electron contexts (e.g. browser testing).

---

## Data model

### `users` table

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL` | PK |
| `username` | `VARCHAR(255)` | Unique |
| `email` | `VARCHAR(255)` | Unique |
| `password_hash` | `VARCHAR(255)` | bcrypt, never returned in responses |
| `created_at` | `TIMESTAMPTZ` | Set by DB default |
| `updated_at` | `TIMESTAMPTZ` | Set by DB default |

---

## Adding new features

1. Add a migration in `backend/migrations/` (goose format)
2. Add/update model structs in `backend/internal/models/`
3. Add CRUD helpers in `backend/internal/db/db.go`
4. Add handler methods in `backend/internal/api/handlers.go`
5. Register routes in `backend/internal/api/router.go`
6. Update `backend/api/openapi.yaml`
7. Add unit tests in `handlers_test.go`, integration tests in `integration_test.go`
8. Update the frontend renderer in `frontend/src/renderer/app.js`
