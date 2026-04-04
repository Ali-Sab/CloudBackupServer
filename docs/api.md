# API Reference

The authoritative machine-readable spec is [`backend/api/openapi.yaml`](../backend/api/openapi.yaml).  
This document provides human-friendly usage notes and `curl` examples.

Base URL (local development): `http://localhost:8080`

---

## Authentication

All protected endpoints use **Bearer token** authentication:

```
Authorization: Bearer <jwt>
```

Tokens are issued by `/api/auth/login` and `/api/auth/register`.  
They expire after **24 hours** and use HS256 signing.

---

## Endpoints

### `GET /api/health`

Returns the server health status. No authentication required.

**Response 200**
```json
{
  "status": "ok",
  "version": "0.1.0"
}
```

---

### `GET /api/session`

Returns the current session state. Always returns `200` — missing or invalid tokens produce `logged_in: false` rather than an error.

**Without token**
```bash
curl http://localhost:8080/api/session
```
```json
{ "logged_in": false }
```

**With valid token**
```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/session
```
```json
{
  "logged_in": true,
  "user": {
    "id": 1,
    "username": "alice",
    "email": "alice@example.com"
  }
}
```

---

### `POST /api/auth/register`

Creates a new user account and returns a JWT.

**Request body**
```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "hunter2"
}
```

**Response 201**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 1,
    "username": "alice",
    "email": "alice@example.com"
  }
}
```

**Error responses**

| Status | Reason |
|--------|--------|
| `400` | Missing or invalid fields |
| `409` | Username or email already in use |

**Example**
```bash
curl -s -X POST http://localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","email":"alice@example.com","password":"hunter2"}' | jq .
```

---

### `POST /api/auth/login`

Authenticates an existing user and returns a JWT.

**Request body**
```json
{
  "username": "alice",
  "password": "hunter2"
}
```

**Response 200**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 1,
    "username": "alice",
    "email": "alice@example.com"
  }
}
```

**Error responses**

| Status | Reason |
|--------|--------|
| `400` | Missing username or password |
| `401` | Wrong username or password |

**Example**
```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"hunter2"}' | jq -r .token)

echo "Token: $TOKEN"
```

---

## Error format

All error responses use the same JSON structure:

```json
{ "error": "human-readable message" }
```

---

## Full workflow example

```bash
BASE="http://localhost:8080"

# Register
curl -s -X POST $BASE/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","email":"alice@example.com","password":"secret"}' | jq .

# Login
TOKEN=$(curl -s -X POST $BASE/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret"}' | jq -r .token)

# Check session
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/session | jq .

# Verify token expiry is ~24h from now
echo $TOKEN | cut -d. -f2 | base64 -d 2>/dev/null | jq .exp | xargs -I{} date -d @{}
```
