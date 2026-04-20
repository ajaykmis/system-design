# Gateway

API Gateway that sits in front of all backend services. Handles routing, authentication, rate limiting, and request logging.

## System Design Concepts

- **Reverse proxy** — single entry point that routes `/api/v1/*` to the appropriate backend service
- **Token bucket rate limiting** — in-memory per-IP limiter (10 req/s sustained, burst of 20). In production this would be Redis-backed for distributed enforcement
- **Auth middleware** — validates JWT access tokens via the Auth service's `/validate` endpoint before forwarding to protected routes
- **Middleware chain** — composable `http.Handler` wrappers: logging → rate limit → auth → proxy

## API

All backend APIs are exposed through the gateway under `/api/v1/`:

| Gateway Path | Backend | Auth Required |
|-------------|---------|---------------|
| `POST /api/v1/register` | Registration :8081 `/register` | No |
| `POST /api/v1/verify` | Registration :8081 `/verify` | No |
| `POST /api/v1/resend` | Registration :8081 `/resend` | No |
| `POST /api/v1/token/refresh` | Auth :8082 `/refresh` | No |
| `POST /api/v1/content` | Ingestion :8090 `/content` | Yes |
| `POST /api/v1/events` | Ingestion :8090 `/events` | Yes |
| `GET /api/v1/feed` | Ranking :8092 `/feed` | Yes |
| `GET /api/v1/debug/*` | Retrieval :8091 `/debug/*` | Yes |
| `GET /health` | Gateway (local) | No |

## Example Flow

```bash
# 1. Register (no auth needed)
curl -X POST http://localhost:8080/api/v1/register \
  -H "Content-Type: application/json" \
  -d '{"phone": "+14155551234", "device_id": "myphone"}'

# 2. Verify (no auth needed)
curl -X POST http://localhost:8080/api/v1/verify \
  -H "Content-Type: application/json" \
  -d '{"request_id": "...", "code": "123456"}'

# 3. Issue tokens (call auth directly for now)
curl -X POST http://localhost:8082/issue \
  -H "Content-Type: application/json" \
  -d '{"user_id": "..."}'

# 4. Access protected endpoint
curl http://localhost:8080/api/v1/feed \
  -H "Authorization: Bearer <access_token>"
```

## Running

```bash
# Prerequisites: registration (:8081) and auth (:8082) services running
cd snapchat/gateway
go run .
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Gateway listen port |
| `REGISTRATION_URL` | `http://localhost:8081` | Registration service URL |
| `AUTH_URL` | `http://localhost:8082` | Auth service URL |
| `INGESTION_URL` | `http://localhost:8090` | Ingestion service URL |
| `RANKING_URL` | `http://localhost:8092` | Ranking service URL |
| `RETRIEVAL_URL` | `http://localhost:8091` | Retrieval service URL |

## Rate Limiting

The gateway uses a **token bucket** algorithm:
- **Rate**: 10 tokens/second (sustained throughput)
- **Burst**: 20 tokens (max concurrent burst)
- **Key**: client IP (`X-Forwarded-For` or `RemoteAddr`)
- Stale buckets are cleaned up every 5 minutes

When rate limited, the response is:
```json
HTTP 429
Retry-After: 10
{"error": "rate limit exceeded"}
```
