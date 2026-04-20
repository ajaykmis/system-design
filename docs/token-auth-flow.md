# Token Authentication — Issue, Validate, Refresh

## Overview

Snapchat uses a **two-token pattern**: a short-lived JWT access token for API calls and a long-lived opaque refresh token to maintain sessions. The API gateway validates tokens centrally and injects `X-Snap-User-ID` so downstream services never handle auth themselves.

## The Two Tokens

```
ACCESS TOKEN (short-lived JWT — 15 min)
───────────────────────────────────────
{
  "header": {"alg": "ES256", "typ": "JWT"},
  "payload": {
    "sub": "user_abc123",          ← user ID
    "iss": "snap-auth",            ← who issued it
    "iat": 1713500000,             ← issued at
    "exp": 1713500900,             ← expires in 15 min
    "scope": ["snap:send", "snap:read", "chat:*"],
    "device_id": "iphone_xyz"      ← bound to this device
  },
  "signature": "<signed with auth service's private key>"
}

Properties:
  ─ Stateless — no DB lookup needed to validate
  ─ Short TTL (15 min) — limits damage if stolen
  ─ Self-contained — has everything the gateway needs
  ─ Never stored server-side — just verify the signature


REFRESH TOKEN (long-lived opaque string — 30 days)
──────────────────────────────────────────────────
"rt_a1b2c3d4e5f6..."   ← random opaque string, NOT a JWT

Properties:
  ─ Stored server-side (hashed in DB)
  ─ Long TTL (30 days) — keeps user logged in
  ─ One-time use — rotated on every refresh
  ─ Bound to device + user
  ─ Can be revoked instantly (delete from DB)
```

### Why Two Tokens?

```
┌──────────────────┬──────────────────┬──────────────────┐
│                  │  Access Token    │  Refresh Token   │
├──────────────────┼──────────────────┼──────────────────┤
│ Format           │ JWT (signed)     │ Opaque string    │
│ TTL              │ 15 minutes       │ 30 days          │
│ Stored server?   │ No               │ Yes (hashed)     │
│ DB lookup?       │ No (verify sig)  │ Yes (every use)  │
│ Revocable?       │ Not instantly*   │ Yes, instantly    │
│ Sent with        │ Every API call   │ Only to /refresh │
│ Stolen impact    │ 15 min window    │ Rotated, detectable│
└──────────────────┴──────────────────┴──────────────────┘

* Access token "revocation" = wait for it to expire (15 min max).
  For emergencies: add to a short-lived deny list at the gateway.
```

## Full Flow

### 1. Login (Token Issuance)

```
POST /auth/issue
{
  "user_id": "user_abc123",
  "device_id": "iphone_xyz"
}

Auth Service:
  1. Verify the user exists + phone was verified
  2. Generate access token (JWT, signed with ES256 private key)
  3. Generate refresh token (crypto-random string)
  4. Hash refresh token with SHA-256
  5. Store hash in refresh_tokens table
  6. Return both tokens to client

Response:
{
  "access_token": "eyJhbGciOiJFUzI1NiIs...",
  "refresh_token": "rt_a1b2c3d4e5f6...",
  "expires_in": 900
}
```

### 2. API Call (Gateway Validates + Injects User ID)

```
Client:
  POST /snap/send
  Authorization: Bearer eyJhbGciOiJFUzI1NiIs...
  Body: {"to": "bob", "content": "..."}
     │
     ▼
API Gateway (Envoy JWT filter):
  1. Extract "Authorization: Bearer <token>" header
  2. Decode JWT header → find "alg": "ES256"
  3. Fetch public key (cached from auth service JWKS endpoint)
     GET http://auth-service/.well-known/jwks.json
     → {"keys": [{"kty":"EC", "kid":"key-1", ...}]}
  4. Verify signature using public key
     ─ Invalid? → 401 immediately
  5. Check claims:
     ─ exp > now? (not expired)
     ─ iss == "snap-auth"? (issued by us)
     ─ scope includes "snap:send"?
  6. Extract "sub" claim → "user_abc123"
  7. SET headers:
     X-Snap-User-ID: user_abc123
     X-Snap-Device-ID: iphone_xyz
  8. STRIP the Authorization header
     (downstream services never see the JWT)
  9. Forward to snap-service
     │
     ▼
Snap Service:
  // Trust the gateway — never validate JWT here
  userID := r.Header.Get("X-Snap-User-ID")
  msg, err := service.SendSnap(userID, req)
```

### 3. Token Refresh (Rotation)

```
POST /auth/refresh
{
  "refresh_token": "rt_old_abc"
}

Auth Service:
  1. Hash "rt_old_abc" with SHA-256
  2. Look up hash in refresh_tokens table
     ─ Not found? → 401 (stolen or already used)
  3. Check: is it expired? revoked?
  4. REVOKE old refresh token (mark revoked=true in DB)
  5. Generate new access token (JWT, 15 min)
  6. Generate new refresh token (random, 30 days)
  7. Store new refresh token hash in DB
     ─ Set replaced_by on old token → points to new token ID
  8. Return both to client

Response:
{
  "access_token": "eyJhbGciOiJFUzI1NiIs...NEW",
  "refresh_token": "rt_new_def789...",
  "expires_in": 900
}

Old refresh token is now DEAD.
```

### 4. Client-Side Token Management

```
Client (iOS/Android SDK):

┌────────────────────────────────────────────────┐
│  Token Manager (in-memory + secure storage)     │
│                                                  │
│  on API call:                                    │
│    if access_token not expired:                  │
│      → use it                                    │
│    else:                                         │
│      → call /auth/refresh with refresh_token     │
│      → store new tokens                          │
│      → retry original request                    │
│                                                  │
│  on 401 response:                                │
│    → call /auth/refresh                          │
│    → if refresh also fails → force re-login      │
│                                                  │
│  Storage:                                        │
│    access_token  → in-memory only (fast access)  │
│    refresh_token → iOS Keychain / Android Keystore│
│                    (encrypted at rest)            │
└────────────────────────────────────────────────┘
```

## Theft Detection via Rotation

```
Normal flow:
  Client has RT_1 → refresh → gets RT_2 → refresh → gets RT_3
  Each old token is revoked. Only the latest works.

Theft scenario:
  Attacker steals RT_1

  Race condition:
    Attacker uses RT_1 → gets RT_2a (RT_1 revoked)
    Client uses RT_1   → FAILS (already revoked!)

    Client can't refresh → forced to re-login
    User notices "logged out" → reports compromise

  OR:
    Client uses RT_1  → gets RT_2 (RT_1 revoked)
    Attacker uses RT_1 → FAILS (already revoked!)

    Auth service detects reuse of revoked token:
    → Revoke ALL tokens for this user + device
    → Force re-authentication
    → Alert security team
```

## Why Centralized Auth at the Gateway

```
Gateway validates JWT ONCE → injects X-Snap-User-ID
Every downstream service trusts the header.

Pros:
  ─ Auth logic in one place (gateway)
  ─ Services are simpler (no JWT library needed)
  ─ Key rotation happens in one place
  ─ Consistent auth policy across all services

Cons:
  ─ Gateway is a single point of trust
  ─ Internal network must be trusted (or use mTLS)

Guards:
  ─ Gateway MUST strip/overwrite X-Snap-User-ID on ingress
    (prevent external clients from forging the header)
  ─ mTLS between services via Envoy sidecars
    (prevent internal service spoofing)
```

## Schema

```sql
-- Refresh tokens (Auth service owns this)
-- Already exists in snapchat/scripts/init_db.sql
CREATE TABLE refresh_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID REFERENCES users(id),
    token_hash      VARCHAR(64) NOT NULL,     -- SHA-256 of the token
    device_id       VARCHAR(128),             -- bound to device
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    revoked         BOOLEAN DEFAULT FALSE,    -- set true on rotation
    replaced_by     UUID                      -- points to the new token
);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_hash ON refresh_tokens(token_hash) WHERE revoked = FALSE;

-- Emergency JWT revocation (deny list for unexpired access tokens)
CREATE TABLE revoked_access_tokens (
    jti             VARCHAR(64) PRIMARY KEY,  -- JWT ID claim
    revoked_at      TIMESTAMPTZ DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL      -- auto-cleanup after JWT expiry
);
```

## Endpoint Summary

| # | Endpoint | What it does |
|---|---|---|
| 1 | `POST /auth/issue` | Verify user, generate access token (JWT) + refresh token, store refresh hash in DB |
| 2 | `POST /auth/refresh` | Validate refresh token, revoke old one, issue new pair (rotation) |
| 3 | Every API call | Gateway validates JWT, extracts `sub` → sets `X-Snap-User-ID`, strips auth header, forwards |
