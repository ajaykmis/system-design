# Ephemeral Messaging — System Design

## Overview

Ephemeral messaging is Snapchat's core feature: content that disappears after being viewed. This MVP implements the key components — per-message encryption, crypto-shredding, blob storage, TTL enforcement, and the full message lifecycle.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                       API Server (:8085)                     │
│                                                              │
│  POST /snap/send     — encrypt, store, create metadata       │
│  POST /snap/open     — decrypt, return content, start timer  │
│  POST /snap/viewed   — expire + crypto-shred                 │
│  GET  /snap/pending  — list unread snaps for a user          │
│  GET  /snap/status   — inspect message + storage state       │
│  GET  /stats         — system overview                       │
└──────────────┬──────────────┬──────────────┬─────────────────┘
               │              │              │
        ┌──────▼──────┐ ┌────▼─────┐ ┌──────▼──────┐
        │  BlobStore   │ │ KeyStore │ │ SnapService │
        │  (S3/GCS)    │ │  (KMS)   │ │  (metadata) │
        │              │ │          │ │             │
        │  Put()       │ │ Generate │ │ SendSnap()  │
        │  Get()       │ │ GetKey() │ │ OpenSnap()  │
        │  Delete()    │ │ Destroy()│ │ ViewComplete│
        └──────────────┘ └──────────┘ │ ExpireByTTL │
                                      └──────┬──────┘
                                             │
                                      ┌──────▼──────┐
                                      │   Reaper    │
                                      │  (cron/bg)  │
                                      │             │
                                      │ Scans for   │
                                      │ expired TTLs│
                                      │ every N sec │
                                      └─────────────┘
```

## Data Flow

### Send a Snap

```
Alice's client
    │
    │  POST /snap/send {from: alice, to: bob, content: <bytes>}
    ▼
┌────────────────────────────────────────────────┐
│ 1. KeyStore.GenerateKey(keyID)                  │
│    → creates AES-256 key, stores in memory      │
│                                                  │
│ 2. Encrypt(content, key)                         │
│    → AES-256-GCM, nonce prepended to ciphertext │
│                                                  │
│ 3. BlobStore.Put(blobRef, encryptedData)         │
│    → stores encrypted blob (S3 in production)    │
│                                                  │
│ 4. Create SnapMessage metadata                   │
│    → state=PENDING, ttl=10s, maxViews=1          │
│    → expiresAt = now + 30 days                   │
│                                                  │
│ 5. Notify Bob (Pub/Sub in production)            │
└────────────────────────────────────────────────┘
```

### Open a Snap

```
Bob's client
    │
    │  POST /snap/open {message_id: xxx, user_id: bob}
    ▼
┌────────────────────────────────────────────────┐
│ 1. Verify bob is the recipient                  │
│ 2. Check state != EXPIRED, viewCount < maxViews │
│ 3. Update state → OPENED, set openedAt          │
│ 4. Update expiresAt = now + ttlAfterOpen        │
│ 5. Increment viewCount                          │
│ 6. KeyStore.GetKey(keyID) → AES key             │
│ 7. BlobStore.Get(blobRef) → encrypted bytes     │
│ 8. Decrypt(encrypted, key) → plaintext          │
│ 9. Return plaintext to Bob's client              │
│10. Notify Alice "Bob opened your snap"           │
└────────────────────────────────────────────────┘
```

### View Complete (Crypto-Shredding)

```
Bob's client (timer expired)
    │
    │  POST /snap/viewed {message_id: xxx, user_id: bob}
    ▼
┌────────────────────────────────────────────────┐
│ 1. Check viewCount >= maxViews                  │
│    YES → proceed to purge                       │
│    NO  → keep alive for remaining views         │
│                                                  │
│ 2. State → EXPIRED                               │
│                                                  │
│ 3. KeyStore.DestroyKey(keyID)                    │
│    → zeroes out key bytes in memory              │
│    → marks key as destroyed                      │
│    → ALL copies of the blob are now unreadable   │
│                                                  │
│ 4. BlobStore.Delete(blobRef)                     │
│    → removes encrypted blob                      │
│    → reclaims storage                            │
└────────────────────────────────────────────────┘
```

## Data Schema

### Message States

```
PENDING ──► DELIVERED ──► OPENED ──► EXPIRED
   │                         │           ▲
   │                         │           │
   └─────────────────────────┴───────────┘
         (TTL reaper can expire from any non-expired state)
```

### Storage Layout (Production)

| Store | What | Lifecycle |
|-------|------|-----------|
| **Message DB** (Postgres) | Metadata: from, to, state, timestamps | Kept for audit; content refs are dead after shredding |
| **Blob Storage** (S3/GCS) | Encrypted media bytes | Deleted when viewed or TTL expires |
| **Key Store** (KMS/Vault) | Per-message AES-256 keys | Destroyed (zeroed) when viewed — crypto-shredding |

### SQL Schema

See `schema.sql` for the full production schema including:
- `snap_messages` — message metadata with state machine
- `encryption_keys` — key lifecycle tracking
- `blob_refs` — blob lifecycle tracking
- `screenshot_events` — social deterrent audit trail
- `expired_snaps` — view for efficient reaper queries

## Components

### BlobStore (`blob_store.go`)
Simulates S3/GCS. In-memory map of `ref → encrypted bytes`. Supports Put, Get, Delete. In production: S3 with server-side encryption, no CDN caching (ephemeral content must not be edge-cached).

### KeyStore (`key_store.go`)
Simulates AWS KMS / HashiCorp Vault. Generates AES-256 keys, retrieves them for decryption, and destroys them (zeroes memory + marks destroyed). Crypto-shredding = destroying the key makes all copies of the encrypted blob permanently unreadable.

### Crypto (`crypto.go`)
AES-256-GCM encryption/decryption. Nonce is prepended to ciphertext. GCM provides both confidentiality and integrity (authenticated encryption).

### SnapService (`snap_service.go`)
Core business logic orchestrating all stores. Handles state transitions, authorization checks, view counting, and triggers crypto-shredding when max views are reached.

### Reaper (`reaper.go`)
Background goroutine that periodically scans for messages past their `expires_at` timestamp. Handles two cases:
1. Snaps that were opened but the client never sent `ViewComplete`
2. Snaps that were never opened within the 30-day window

### API Server (`main.go`)
HTTP endpoints + notification callbacks (Pub/Sub stubs). Includes a demo page with curl examples at `/`.

## How to Run

```bash
# Fetch dependencies
go get github.com/google/uuid

# Run the server
go run ./snapchat/ephemeral/

# In another terminal, run the demo flow:

# 1. Send a snap (Alice → Bob)
curl -s -X POST http://localhost:8085/snap/send \
  -H 'Content-Type: application/json' \
  -d '{"from_user_id":"alice","to_user_id":"bob","content":"aGVsbG8gYm9i","ttl_after_open":10,"max_views":1}'

# 2. Check Bob's pending snaps
curl -s http://localhost:8085/snap/pending?user_id=bob | jq

# 3. Open the snap (use the ID from step 1)
curl -s -X POST http://localhost:8085/snap/open \
  -H 'Content-Type: application/json' \
  -d '{"message_id":"<ID>","user_id":"bob"}'

# 4. Report view complete (triggers crypto-shredding)
curl -s -X POST http://localhost:8085/snap/viewed \
  -H 'Content-Type: application/json' \
  -d '{"message_id":"<ID>","user_id":"bob"}'

# 5. Verify the snap is gone
curl -s http://localhost:8085/snap/status?id=<ID> | jq
# → blob_exists: false, key_destroyed: true

# 6. Try to open again (should fail)
curl -s -X POST http://localhost:8085/snap/open \
  -H 'Content-Type: application/json' \
  -d '{"message_id":"<ID>","user_id":"bob"}'
# → error: snap has expired
```

## Production Considerations

| Concern | MVP | Production |
|---------|-----|-----------|
| Blob storage | In-memory map | S3/GCS with no CDN caching |
| Key management | In-memory map | AWS KMS / Vault with HSM backing |
| Message DB | In-memory map | PostgreSQL with row-level TTL |
| Reaper | Goroutine every 5s | Separate service + DB query on `expired_snaps` view |
| Notifications | Callback functions | Redis Pub/Sub or Kafka for cross-server delivery |
| Auth | None (user_id in request) | JWT validation on every request |
| Encryption | AES-256-GCM | Same, but key wrapping with master key in KMS |
| Screenshot detection | Not implemented | OS-level hooks (iOS/Android) + event logging |
| Rate limiting | None | Token bucket per user on send/open |
