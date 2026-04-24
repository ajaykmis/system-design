# Snapchat MVP — System Design Walkthrough

A local Snapchat-like application built to learn and demonstrate system design concepts. Two features drive the architecture: **phone-based registration** and **Spotlight content discovery** (ranking pipeline with ANN retrieval).

Go for latency-sensitive infra services. Python for ML/data pipeline services.

---

## Step 1: Understand the Architecture

```
  Client (curl)
       │
       ▼
┌──────────────┐
│   Gateway    │ :8080  (Go)
│  rate limit  │ ← Token bucket: 10 req/s, burst 20
│  auth check  │ ← Validates JWT via Auth service
└──────┬───────┘
       │ routes /api/v1/* to backends
       │
  ┌────┼────────────────────┐
  │    │                    │
  ▼    ▼                    ▼
┌─────────────┐ ┌────────┐ ┌──────────┐
│Registration │ │  Auth  │ │Ingestion │
│   :8081     │ │ :8082  │ │  :8090   │
│    (Go)     │ │  (Go)  │ │ (Python) │
└──────┬──────┘ └───┬────┘ └─┬────┬───┘
       │            │        │    │
       ▼            ▼        │    ▼
  ┌──────────────────----│   |   Kafka
  │    PostgreSQL :5433  │   │  ┌─────────────────┐
  │  users               │   │  │ content-raw     │──→ Retrieval :8091
  │  verification_codes  │   │  │ engagement-events│──→ Feature Pipeline
  │  content             │   │  └─────────────────┘         │
  │  refresh_tokens      │   │                               ▼
  └──────────────────────┘   │                          ┌──────┐
                             │                          │Redis │ :6380
                             │                          │features:{id}│
       ┌─────────────────────┘                          └──┬───┘
       ▼                                                   │
  ┌──────────┐                                             │
  │Retrieval │ :8091 (Python)                              │
  │  HNSW    │ ← hnswlib index (M=16, ef=50)               │
  │  leader  │ ← Redis SETNX election                      │ 
  │  hash    │ ← Consistent hash ring                      │
  └────┬─────┘                                             │
       │ top-100 candidates                                │
       ▼                                                   │
  ┌──────────┐                                             │
  │ Ranking  │ :8092 (Python)  ←───────────────────────────┘
  │  score   │ ← Combines ANN distance + Redis features
  │  circuit │ ← Circuit breaker on Retrieval calls
  └──────────┘
       │
       ▼
   Ranked feed (top-20)
```

---

## Step 2: Trace the Two Main Flows

### Flow A: Phone Registration

```
1. POST /api/v1/register  {"phone": "+14155551234"}
   │
   ├─ Gateway: rate limit check (token bucket, per-IP)
   ├─ Gateway: public path → skip auth
   │
   ▼
2. Registration service:
   ├─ Validate phone format (E.164 regex)
   ├─ Rate limit: max 5 codes per phone per hour (DB count)
   ├─ Generate 6-digit code (crypto/rand)
   ├─ Store in PostgreSQL: verification_codes table
   │   (request_id, phone, code, attempts=0, max_attempts=3, expires_at=now+5min)
   ├─ Mock SMS: print code to stdout
   └─ Return {request_id, expires_in: 300}

3. POST /api/v1/verify  {"request_id": "...", "code": "123456"}
   │
   ▼
4. Registration service:
   ├─ Lookup verification_codes by request_id
   ├─ Check: not expired? not already verified? attempts < 3?
   ├─ Increment attempts (BEFORE checking code — prevents timing attacks)
   ├─ Compare code
   ├─ If match: mark verified, create user (INSERT ON CONFLICT)
   │   → users table: {id, phone, phone_hash (SHA-256)}
   └─ Return {user_id}

5. POST /issue  {"user_id": "..."}  (call Auth directly)
   │
   ▼
6. Auth service:
   ├─ Generate access token (JWT, 15 min, HMAC-SHA256)
   ├─ Generate refresh token (JWT, 7 days)
   ├─ Store refresh token hash (SHA-256) in PostgreSQL
   └─ Return {access_token, refresh_token, expires_in: 900}
```

**Key files:**
- `registration/handler.go` — register, verify, resend handlers
- `registration/store.go` — PostgreSQL queries, phone hashing
- `registration/sms.go` — MockSMS provider (prints code to stdout)
- `auth/jwt.go` — JWT generation and validation
- `auth/handler.go` — issue, refresh, validate endpoints

### Flow B: Spotlight Feed (the interesting one)

```
1. POST /api/v1/content  {"title": "Funny cat video", "category": "comedy"}
   │
   ├─ Gateway: auth check → calls Auth /validate → gets X-User-ID
   │
   ▼
2. Ingestion service:
   ├─ Generate 128-dim embedding from title+category
   │   └─ embedder.py: 70% category centroid + 30% text hash vector
   │      (comedy content clusters together in embedding space)
   ├─ Store in PostgreSQL: content table (metadata + embedding as BYTEA)
   ├─ Publish to Kafka topic "content-raw":
   │   {content_id, creator_id, title, category, embedding: [0.1, -0.2, ...]}
   └─ Return {content_id}

3. Retrieval service (background Kafka consumer):
   ├─ Leader election: only the leader processes Kafka messages
   │   └─ Redis SETNX "leader:index-builder" with 30s TTL
   │   └─ Renews lease every 10s (TTL/3)
   ├─ Consume from "content-raw" topic
   ├─ Add embedding to HNSW index (incremental insert)
   │   └─ hnswlib: M=16 connections, ef_construction=200
   └─ On startup: bootstrap index from all existing PostgreSQL content

4. POST /api/v1/events  {"content_id": "...", "event_type": "view"}
   │
   ▼
5. Ingestion service:
   └─ Publish to Kafka topic "engagement-events"

6. Feature Pipeline (background process):
   ├─ Consume from "engagement-events"
   ├─ Tumbling window (60s): count events per content per minute
   ├─ Sliding window (1 hour): rolling event counts via deque
   │   └─ Old entries evicted as they fall outside the window
   ├─ Every 5 seconds: flush aggregates to Redis
   │   └─ HSET features:{content_id} view_count_1h 1500 like_count_1h 75 ...
   └─ Keys expire after 2 hours (stale features disappear)

7. GET /api/v1/feed?limit=20
   │
   ├─ Gateway: auth check → X-User-ID
   │
   ▼
8. Ranking service:
   ├─ Get user embedding (avg of their content, or random for new users)
   │
   ├─ Call Retrieval /retrieve (protected by circuit breaker)
   │   ├─ Circuit CLOSED: normal call
   │   ├─ Circuit OPEN (3+ failures): skip, return empty → degraded feed
   │   └─ Circuit HALF_OPEN (after 15s cooldown): try one request
   │
   ▼
9. Retrieval service:
   ├─ HNSW knn_query(user_embedding, k=100, ef_search=50)
   │   └─ Searches ~50 graph neighbors per query (recall vs latency knob)
   └─ Return [{content_id, distance}, ...] sorted by L2 distance

10. Ranking service (continued):
    ├─ Batch-fetch content metadata from PostgreSQL
    │   └─ title, category, creator_id, age_hours
    ├─ Batch-fetch real-time features from Redis (pipelined)
    │   └─ view_count_1h, like_count_1h, share_count_1h
    ├─ Score each candidate:
    │   score = 0.40 * (1 / (1 + ann_distance))     # relevance
    │         + 0.25 * (log(1 + views) / 10)         # popularity
    │         + 0.20 * ((likes + shares) / views)     # engagement
    │         + 0.15 * exp(-0.693 * age_hours / 24)   # freshness
    ├─ Sort by score descending
    └─ Return top-20 with scores and feature breakdowns
```

**Key files:**
- `ingestion/embedder.py` — mock embedding with category clustering
- `ingestion/main.py` — FastAPI, Kafka producer
- `retrieval/index.py` — HNSW index (hnswlib wrapper)
- `retrieval/leader.py` — Redis leader election
- `retrieval/consistent_hash.py` — hash ring for shard mapping
- `feature_pipeline/windows.py` — tumbling + sliding window implementations
- `feature_pipeline/main.py` — Kafka consumer → Redis sink
- `ranking/ranker.py` — scoring formula
- `ranking/circuit_breaker.py` — circuit breaker (closed/open/half-open)
- `ranking/main.py` — feed orchestration

---

## Step 3: Understand Each System Design Concept

### 1. Pub/Sub & Message Queues (Kafka)

**Where:** Ingestion → Kafka → Retrieval + Feature Pipeline

Two topics decouple producers from consumers:
- `content-raw`: Ingestion publishes, Retrieval consumes for indexing
- `engagement-events`: Ingestion publishes, Feature Pipeline consumes for aggregation

Multiple consumer groups process the same stream independently. Adding a new consumer (e.g., analytics) doesn't affect existing ones.

**Try it:**
```bash
# Publish content → see it appear in Kafka → Retrieval indexes it
curl -X POST http://localhost:8090/content \
  -H "X-User-ID: <id>" -H "Content-Type: application/json" \
  -d '{"title": "Test", "category": "comedy"}'
# Check retrieval logs: "Indexed content <id> from Kafka"
```

### 2. Stream Processing (Feature Pipeline)

**Where:** `feature_pipeline/` — Kafka consumer with windowed aggregation

Implements the same semantics as Apache Flink, but with visible internals:
- **Tumbling window (60s):** Fixed non-overlapping windows. Counter resets each minute.
- **Sliding window (1 hour):** Deque of (timestamp, event_type). Old entries evicted on read.
- **Materialized view:** Every 5s, aggregates flushed to Redis hashes.

| Aspect | This Implementation | Production Flink |
|--------|-------------------|-----------------|
| Parallelism | Single thread | Parallel operators |
| Fault tolerance | Restart from earliest | Checkpointing + exactly-once |
| Windowing | Manual deque eviction | Built-in window operators |
| State | In-memory dict | RocksDB state backend |

**Try it:**
```bash
# Send engagement events
for i in $(seq 1 20); do
  curl -s -X POST http://localhost:8090/events \
    -H "X-User-ID: <id>" -H "Content-Type: application/json" \
    -d '{"content_id": "<id>", "event_type": "view"}'
done
# Wait 6 seconds, then check Redis
redis-cli -p 6380 HGETALL "features:<content_id>"
```

### 3. ANN Search (HNSW)

**Where:** `retrieval/index.py` — hnswlib wrapper

HNSW builds a multi-layer graph where each node connects to M nearest neighbors. Queries navigate the graph from top (sparse) to bottom (dense) layers.

**Parameters and their trade-offs:**
- `M=16` — connections per node. Higher = better recall, more memory
- `ef_construction=200` — build-time beam width. Higher = better index quality
- `ef_search=50` — query-time beam width. **This is the recall vs latency knob**

**Why HNSW over IVF/PQ:**
- Better recall at same latency (graph vs. partition-based)
- Supports incremental inserts (no full rebuild needed)
- Same approach used at Snap for Spotlight retrieval

**Try it:**
```bash
curl http://localhost:8091/debug/index
# {"total_items": 50, "M": 16, "ef_search": 50, "space": "l2", "dim": 128}
```

### 4. Database Design

**Where:** `scripts/init_db.sql`, Redis key patterns

**PostgreSQL (persistent, ACID):**
- `users` — phone, phone_hash (SHA-256 for safe lookups)
- `verification_codes` — request_id, code, attempts, expires_at
- `content` — title, category, embedding (BYTEA), creator_id
- `refresh_tokens` — token_hash (never store plaintext), revoked flag

**Redis (ephemeral, fast):**
- `features:{content_id}` — HASH with view_count_1h, like_count_1h (TTL 2h)
- `leader:index-builder` — STRING with node_id (TTL 30s, SETNX)
- Rate limit counters (in-memory in MVP, Redis-backed in production)

**Design principle:** Persistent data in PostgreSQL, ephemeral/computed data in Redis.

### 5. Consistent Hashing

**Where:** `hasher/ring.go` (Go lib), `retrieval/consistent_hash.py` (Python)

Hash ring with 150 virtual nodes per physical node. Keys map to the next clockwise node on the ring.

**Why it matters:** When a retrieval shard is added/removed, only K/N keys need to move (not all of them like with modular hashing).

**Tested results:**
- 3 nodes, 10K keys → 3257/3393/3350 distribution (~33.3% each)
- Adding a 4th node → 256 of 1000 keys moved (expected 250 = K/N)

**Try it:**
```bash
curl http://localhost:8091/debug/ring
# {"nodes": ["retrieval-1"], "vnodes_per_node": 150, "total_vnodes": 150}

cd snapchat/hasher && go test -v ./...
```

### 6. Leader Election

**Where:** `election/elector.go` (Go lib), `retrieval/leader.py` (Python)

Redis SETNX + TTL pattern:
1. Node calls `SETNX leader:index-builder <node_id> EX 30`
2. If set → this node is the leader
3. Leader renews lease every 10s (TTL/3)
4. If leader dies → TTL expires → another node's SETNX succeeds

Only the leader consumes from Kafka and updates the HNSW index.

**Try it:**
```bash
curl http://localhost:8091/debug/leader
# {"node_id": "retrieval-1", "is_leader": true, "term": 1, "lease_remaining": 21}

cd snapchat/election && go test -v ./...
```

### 7. Rate Limiting & Backpressure

**Where:** `gateway/ratelimit.go` (token bucket), `ranking/circuit_breaker.py`

**Token bucket (Gateway):**
- 10 tokens/second refill rate, 20 token burst capacity
- Per-IP keying (X-Forwarded-For or RemoteAddr)
- Stale buckets cleaned up every 5 minutes

**Circuit breaker (Ranking → Retrieval):**
- CLOSED: requests pass through
- OPEN (after 3 failures): fail fast, return empty feed (graceful degradation)
- HALF_OPEN (after 15s cooldown): try one request, close if it succeeds

**Try it:**
```bash
curl http://localhost:8092/debug/circuit
# {"state": "closed", "failure_count": 0, "failure_threshold": 3}
```

### 8. Metrics / Event Aggregation

**Where:** Feature Pipeline + Kafka engagement-events topic

The pattern: raw events → stream processor → materialized view → query service

```
User views content
  → POST /events → Kafka "engagement-events"
  → Feature Pipeline: tumbling (1min) + sliding (1hr) windows
  → Redis HSET features:{id} view_count_1h 500 like_count_1h 25
  → Ranking service reads features to boost popular content
```

This is the same architecture used for real-time metrics at scale. The stream processor is the only thing that writes to the feature store; the ranking service only reads.

---

## Step 4: Run It Yourself

### Prerequisites
```bash
docker compose up -d   # starts PostgreSQL :5433, Redis :6380, Kafka :29092
```

### Start services (each in a separate terminal, or background them)
```bash
cd snapchat/registration && go run .          # :8081
cd snapchat/auth && go run .                  # :8082
cd snapchat/gateway && go run .               # :8080
cd snapchat/ingestion && uvicorn main:app --port 8090   # :8090
cd snapchat/retrieval && uvicorn main:app --port 8091   # :8091
cd snapchat/ranking && uvicorn main:app --port 8092     # :8092
cd snapchat/feature_pipeline && python main.py           # background
```

### Seed content
```bash
# Register a user first
USER_ID=$(... register + verify flow ...)
python scripts/seed_content.py <user_id> 100
```

### Run E2E test
```bash
bash scripts/test_flow.sh
```

### Explore
```bash
# Debug endpoints
curl http://localhost:8091/debug/index    # HNSW stats
curl http://localhost:8091/debug/ring     # consistent hash ring
curl http://localhost:8091/debug/leader   # leader election state
curl http://localhost:8092/debug/circuit  # circuit breaker state

# Query the database
docker exec -it snap-postgres psql -U snapuser -d snapchat
\dt                              # list tables
SELECT * FROM users;             # see registered users
SELECT title, category FROM content LIMIT 10;

# Check Redis features
docker exec snap-redis redis-cli KEYS "features:*"
docker exec snap-redis redis-cli HGETALL "features:<content_id>"
```

---

## Step 5: Map to Interview Questions

| Interview Question | What to talk about | Where in the code |
|---|---|---|
| "Design a content discovery system" | Two-stage retrieval+ranking, HNSW, feature assembly | `retrieval/`, `ranking/` |
| "How would you handle millions of events per second?" | Kafka partitioning, stream processing windows, materialized views | `feature_pipeline/`, `ingestion/` |
| "Design a rate limiter" | Token bucket algorithm, per-key bucketing, distributed via Redis | `gateway/ratelimit.go` |
| "How do you handle leader election?" | Redis SETNX+TTL, lease renewal, graceful resignation | `election/`, `retrieval/leader.py` |
| "How do you shard data?" | Consistent hash ring, virtual nodes, K/N redistribution | `hasher/`, `retrieval/consistent_hash.py` |
| "Design phone-based registration" | SMS verification, attempt limiting, rate limiting, PII hashing | `registration/` |
| "How do you handle service failures?" | Circuit breaker pattern, graceful degradation | `ranking/circuit_breaker.py` |
| "Design a real-time feature store" | Event stream → windowed aggregation → Redis materialization | `feature_pipeline/` |

---

## Observability — Metrics

All Go and Python HTTP services are instrumented with Prometheus client libraries. A shared monitoring stack in `monitoring/` scrapes all services via the pull model.

### Pull vs Push model

We use **pull** (Prometheus scrapes every 15s). All services are long-running, so the pull model is natural:
- Each service can be debugged with `curl :PORT/metrics` independently
- No collector address baked into service code
- Prometheus controls scrape rate; services are never blocked

**Scaling boundary:** At 100K pods with high-cardinality labels, pull breaks. The production approach (Snap-style) is push with sidecar pre-aggregation: `StatsD UDP → Envoy sidecar → M3DB`. See `docs/superpowers/specs/2026-04-24-metrics-monitoring-design.md`.

### gateway (Go) — metrics port `:9200`

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_http_requests_total` | Counter | method, path, status | Total proxied requests |
| `snap_http_request_duration_seconds` | Histogram | method, path | End-to-end proxy latency |
| `snap_rate_limit_rejections_total` | Counter | — | Requests rejected by token bucket |
| `snap_auth_failures_total` | Counter | — | Failed JWT validations |

### registration (Go) — metrics port `:9201`

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_registrations_total` | Counter | status (success/failure) | Registration attempts |
| `snap_sms_sent_total` | Counter | — | OTP SMS dispatched |

### auth (Go) — metrics port `:9202`

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_tokens_issued_total` | Counter | type (access/refresh) | Tokens minted |
| `snap_auth_failures_total` | Counter | reason (invalid_creds/expired) | Auth failures |

### ingestion (Python) — metrics port `:9203`

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_http_requests_total` | Counter | method, path, status | HTTP requests to ingestion |
| `snap_snaps_uploaded_total` | Counter | — | Snap content accepted |
| `snap_kafka_events_produced_total` | Counter | — | Events published to Kafka |

### Alert rules (evaluated by Prometheus every 1m)

| Rule | Condition | Severity |
|---|---|---|
| `SnapHighErrorRate` | HTTP 5xx rate > 5% for 5m | warning |
| `SnapRateLimitSpike` | rate_limit_rejections > 10/s for 2m | warning |
| `SnapHighLatencyP99` | p99 proxy latency > 1s for 5m | warning |
| `SnapAuthFailureSpike` | auth_failures > 5/s for 2m | critical |
