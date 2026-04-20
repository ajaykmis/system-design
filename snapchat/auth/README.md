# Auth Service

JWT-based authentication. Issues access/refresh token pairs, validates tokens for the Gateway, and supports token rotation.

## System Design Concepts

- **Short-lived access tokens** (15 min) — limits damage window if a token leaks
- **Refresh token rotation** — old refresh token is revoked when a new pair is issued, preventing replay attacks
- **Token hashing** — refresh tokens stored as SHA-256 hashes in the DB, never in plaintext
- **Stateless validation** — access tokens are validated purely via HMAC signature, no DB call needed

## API

### POST /issue
Create a new token pair for a user. Called internally by the Registration service after phone verification.

```bash
curl -X POST http://localhost:8082/issue \
  -H "Content-Type: application/json" \
  -d '{"user_id": "uuid"}'
```

**Response** `200`:
```json
{
  "access_token": "eyJ...",
  "refresh_token": "eyJ...",
  "expires_in": 900
}
```

**Errors**: `404` user not found

### POST /refresh
Exchange a valid refresh token for a new token pair (rotation).

```bash
curl -X POST http://localhost:8082/refresh \
  -H "Content-Type: application/json" \
  -d '{"refresh_token": "eyJ..."}'
```

**Response** `200`: same as `/issue`

**Errors**: `401` invalid/revoked/expired refresh token

### GET /validate
Check if an access token is valid. Used by the Gateway for auth middleware.

```bash
curl http://localhost:8082/validate \
  -H "Authorization: Bearer eyJ..."
```

**Response** `200`:
```json
{"valid": true, "user_id": "uuid"}
```
or
```json
{"valid": false}
```

### GET /health
```bash
curl http://localhost:8082/health
# {"status": "ok"}
```

## Running

```bash
# Prerequisites: PostgreSQL running on port 5433 (via docker-compose)
cd snapchat/auth
go run .
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_DSN` | `postgres://snapuser:snappass@localhost:5433/snapchat?sslmode=disable` | PostgreSQL connection string |
| `JWT_SECRET` | `dev-secret-do-not-use-in-prod` | HMAC signing key for JWTs |
| `PORT` | `8082` | HTTP listen port |

## Database Tables

- `refresh_tokens` — hashed refresh tokens with user_id, expiry, and revoked flag
- `users` — read-only access to verify user existence

## Token Lifecycle

```
Register phone → Verify code → /issue → access_token (15m) + refresh_token (7d)
                                              │
                                    token expires
                                              │
                                        /refresh → new access_token + new refresh_token
                                              │    (old refresh_token revoked)
```
