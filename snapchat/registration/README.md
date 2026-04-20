# Registration Service

Phone-based user registration with SMS verification. Implements the same flow used at Snap — send a code, verify it, create the user.

## System Design Concepts

- **Rate limiting** — max 5 verification codes per phone per hour (DB-level counting)
- **Attempt tracking** — max 3 tries per code to prevent brute-force
- **PII hashing** — phone numbers stored alongside a SHA-256 hash for safe lookups
- **Idempotent user creation** — `ON CONFLICT DO UPDATE` ensures re-verification of an existing phone doesn't fail

## API

### POST /register
Request a verification code for a phone number.

```bash
curl -X POST http://localhost:8081/register \
  -H "Content-Type: application/json" \
  -d '{"phone": "+14155551234", "device_id": "myphone"}'
```

**Response** `200`:
```json
{"request_id": "uuid", "expires_in": 300}
```

**Errors**: `400` invalid phone, `429` rate limited

### POST /verify
Submit the 6-digit code to complete registration.

```bash
curl -X POST http://localhost:8081/verify \
  -H "Content-Type: application/json" \
  -d '{"request_id": "uuid", "code": "123456"}'
```

**Response** `200`:
```json
{"user_id": "uuid"}
```

**Errors**: `401` wrong code, `404` unknown request_id, `410` expired/already used, `429` too many attempts

### POST /resend
Request a new code for an existing verification (creates a new request_id).

```bash
curl -X POST http://localhost:8081/resend \
  -H "Content-Type: application/json" \
  -d '{"request_id": "uuid"}'
```

### GET /health
```bash
curl http://localhost:8081/health
# {"status": "ok"}
```

## Running

```bash
# Prerequisites: PostgreSQL running on port 5433 (via docker-compose)
cd snapchat/registration
go run .
```

The mock SMS provider prints the verification code to stdout — no real SMS is sent.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_DSN` | `postgres://snapuser:snappass@localhost:5433/snapchat?sslmode=disable` | PostgreSQL connection string |
| `PORT` | `8081` | HTTP listen port |

## Database Tables

- `users` — created user records (id, phone, phone_hash)
- `verification_codes` — pending/completed verifications (request_id, code, attempts, expires_at)
