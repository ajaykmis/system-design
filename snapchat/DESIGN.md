# Snapchat MVP — Local System Design Learning App

## Context

Build a local Snapchat-like application to learn and demonstrate system design concepts hands-on. Two features drive the architecture: **phone-based registration** and **Spotlight content discovery** (ranking pipeline with ANN retrieval). Every component maps to a specific Staff-level system design concept.

Everything runs locally via `docker-compose`. Go for latency-sensitive infra services, Python for ML/data pipeline services.

---

## Architecture Overview

```
                         ┌──────────────┐
                         │   Gateway    │ :8080  (Go)
                         │  rate limit  │
                         │  auth check  │
                         └──────┬───────┘
                    ┌───────────┼───────────┐
                    v           v           v
            ┌────────────┐ ┌────────┐ ┌──────────┐
            │Registration│ │  Auth  │ │Ingestion │
            │   :8081    │ │ :8082  │ │  :8090   │
            │    (Go)    │ │  (Go)  │ │ (Python) │
            └─────┬──────┘ └───┬────┘ └────┬─────┘
                  │            │      Kafka │
            ┌─────┴────────────┴──┐   ┌────┴──────────┐
            │    PostgreSQL       │   │  content-raw   │──→ Retrieval :8091 (HNSW)
            │    Redis            │   │  engage-events │──→ Flink (feature pipeline)
            └─────────────────────┘   └────────────────┘       │
                                                               v
                                           ┌──────────┐    ┌──────┐
                                           │ Ranking  │←───│Redis │ (features)
                                           │  :8092   │    └──────┘
                                           └──────────┘
```

## Services

| Service | Lang | Port | Demonstrates |
|---------|------|------|-------------|
| **Gateway** | Go | 8080 | Rate limiting, backpressure, reverse proxy |
| **Registration** | Go | 8081 | Phone verification flow, token bucket rate limiting |
| **Auth** | Go | 8082 | JWT issuance/validation, refresh tokens |
| **Rate Limiter** | Go | lib | Token bucket + sliding window (Redis-backed) |
| **Ingestion** | Python | 8090 | Kafka producer, embedding generation, pub/sub |
| **Retrieval** | Python | 8091 | **HNSW ANN search**, consistent hashing, leader election |
| **Ranking** | Python | 8092 | Feature assembly, scoring, ML pipeline |
| **Feature Pipeline** | Python | — | PyFlink, stream processing, windowed aggregation |

**Infrastructure:** PostgreSQL 16, Redis 7, Kafka (KRaft mode), Flink 1.18

## Directory Structure

```
snapchat/
  DESIGN.md                    # Full architecture + theory (metrics/event aggregation)
  docker-compose.yml
  Makefile
  chat/                        # EXISTING — preserve as-is
  scripts/
    init_db.sql                # PostgreSQL schema
    seed_content.py            # Generate fake content + embeddings
    test_flow.sh               # E2E smoke test
  gateway/                     # Go — reverse proxy, middleware
  registration/                # Go — phone verify, mock SMS
  auth/                        # Go — JWT, refresh tokens
  ratelimiter/                 # Go — token bucket + sliding window lib
  hasher/                      # Go — consistent hash ring lib + tests
  election/                    # Go — Redis leader election lib + tests
  ingestion/                   # Python — FastAPI, Kafka producer, embedder
  retrieval/                   # Python — HNSW index, ANN search, consistent hash, leader election
  ranking/                     # Python — feature assembly, scoring, feed endpoint
  feature_pipeline/            # Python — PyFlink job, windowed aggregations
  proto/                       # JSON schemas for content/user events
```

## Concept-to-Component Mapping

| Concept | Where | Interview talking point |
|---------|-------|----------------------|
| **Pub/Sub & Message Queues** | Kafka topics, ingestion producer, retrieval consumer | Decoupled ingestion from indexing; multiple consumer groups process same stream |
| **Stream Processing** | PyFlink tumbling/sliding windows on engagement events | Real-time feature materialization to Redis feature store |
| **ANN Search** | HNSW index (hnswlib) in retrieval service | Hierarchical navigable small-world graph; `ef_search` controls recall vs latency trade-off |
| **Database Design** | PostgreSQL schemas + Redis key design | Persistent (users, content) vs ephemeral (features, rate limits, sessions) |
| **Consistent Hashing** | Hash ring in retrieval for content-to-shard mapping | Adding/removing nodes only redistributes K/N items |
| **Leader Election** | Redis SETNX+TTL for HNSW index builder coordination | Leader rebuilds index, followers load from shared volume; lease expiry handles failures |
| **Rate Limiting & Backpressure** | Token bucket on SMS, sliding window on API, circuit breaker on ranking | Graceful degradation: cached feed when retrieval is slow |
| **Metrics / Event Aggregation** | Theory in DESIGN.md + engagement Kafka topic + Flink pipeline | Events → Kafka → Flink windows → Redis materialized views |

## Retrieval Pipeline (Deep Focus)

1. **Embedding generation** — Mock 128-dim normalized vectors (hash-based for reproducibility)
2. **HNSW index building** — `hnswlib.Index(space='l2', dim=128)` with `M=16, ef_construction=200`; leader builds periodically from Kafka stream
3. **Consistent hash ring** — Maps `content_id` to shard; debug endpoint shows ring state
4. **Leader election** — Redis SETNX, 30s TTL lease, leader writes serialized index to shared volume
5. **Candidate generation** — Query HNSW with user embedding (`ef_search=50`), return top-100 with L2 distances
6. **Ranking** — Combine ANN distance + real-time features (view_count, engagement_rate, freshness) → score → top-20

HNSW is the right fit here (vs IVF/PQ). The graph structure provides better recall at the same latency compared to partition-based approaches, and supports incremental inserts without full index rebuilds.

## Key Data Design

**Kafka topics:** `content-raw` (6 partitions, keyed by content_id), `engagement-events` (12 partitions, keyed by content_id), `user-events` (6 partitions), `index-commands` (1 partition)

**PostgreSQL tables:** `users`, `verification_codes`, `content`, `refresh_tokens`

**Redis keys:** `ratelimit:*` (counters), `features:{content_id}` (hash), `leader:index-builder` (SETNX), `session:{user_id}`, `index:version`

## Implementation Phases

### Phase 1: Foundation
- docker-compose with postgres, redis, kafka
- `init_db.sql` schema
- Registration service (handlers, mock SMS, PostgreSQL)
- Auth service (JWT, refresh tokens)
- Rate limiter library (token bucket + sliding window, Redis-backed)
- Gateway (reverse proxy, rate limit + auth middleware)
- **Verify:** Register phone → verify code → get JWT → call protected endpoint

### Phase 2: Content Pipeline
- Ingestion service (FastAPI, mock embedder, Kafka producer)
- Retrieval service (Kafka consumer, HNSW index builder, search endpoint)
- Consistent hash ring (Go lib + Python implementation)
- Seed script (1000 fake content items)
- **Verify:** Seed content → index builds → query retrieval for candidates

### Phase 3: Ranking + Features
- Ranking service (feature assembly, scoring, feed endpoint)
- Feature pipeline (PyFlink job, engagement events → Redis)
- Engagement event ingestion endpoint
- **Verify:** Seed content → generate engagement events → request feed → ranked results

### Phase 4: Distributed Systems + Docs
- Leader election integration for index builder
- Circuit breaker in ranking service
- Dockerfiles for all services, full docker-compose wiring
- Complete DESIGN.md with theory sections, metrics/event aggregation design
- E2E test script
- **Verify:** `docker-compose up` → `test_flow.sh` passes

## Mock Content Strategy
- No actual video files — just metadata + 128-dim HNSW embeddings
- Embeddings generated deterministically from title text (hash-seeded, L2-normalized)
- Categories (comedy, sports, music, food, etc.) clustered in embedding space for meaningful ANN results
- ~50 fake users, each creating 10-20 content items
- Engagement events follow power-law distribution (some content goes viral)
- `media_url` is a placeholder string (`mock://videos/abc123.mp4`)
