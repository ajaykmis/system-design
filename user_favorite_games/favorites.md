Favorites Service (Roblox-style Games)

A clean, production-ready design for favorite/unfavorite with low-latency reads, scalable counters, and trending leaderboards.

Goals & Scope

User stories

Favorite / Unfavorite a game (idempotent).

See my favorite games (paged, newest first).

See whether I favorited a specific game.

See favorite count for a game.

See Top-K trending / all-time leaderboard.

Non-functional

P99 < 100ms for reads, high availability (multi-AZ).

Read-heavy (up to ~1M QPS), ~100k QPS writes.

Abuse controls, auditability, GDPR delete ready.

Assumptions (tunable)

Store: DynamoDB (source of truth) + Redis (cache & counters).

Stream: Kafka/PubSub (events, backfills, leaderboards).

Optional reverse index (game→users) for moderation/offline jobs.

High-Level Architecture
flowchart LR
  subgraph Client
    A[Web / Mobile App]
  end

  subgraph Edge
    B[API Gateway<br/>AuthN/Rate limit]
  end

  subgraph Service
    C[Favorites Service<br/>Stateless API]
  end

  subgraph HotPath
    D[(Redis Cluster)]
    E[(Sharded Counter Keys)]
    Z[(Redis ZSETs<br/>(Leaderboards))]
  end

  subgraph SourceOfTruth
    F[(DynamoDB<br/>favorites_by_user)]
    G[(DynamoDB<br/>game_fav_counters)]
  end

  subgraph Stream
    H[[Kafka / PubSub<br/>FavoriteCreated/Deleted]]
  end

  subgraph Workers
    I[Counter Compactor<br/>(Redis→DDB)]
    J[Leaderboard Builder<br/>(windowed ZSETs)]
    K[Warehouse ETL<br/>(analytics)]
  end

  subgraph Adjacent
    M[Game Metadata Service<br/>(BatchGet)]
  end

  A --> B --> C
  C <--> D
  C --> F
  C --> H
  C --> E
  C <---> M
  I --> G
  H --> I
  H --> J
  J --> Z
  C --> Z
  D -->|memoized sums| C
  E -->|sum shards| C


Key ideas

User→Games index is authoritative.

Sharded counters in Redis for hot, concurrent increments; compacted to durable counters.

Leaderboards via Redis ZSETs, refreshed by stream workers.

Events make everything auditable & backfillable.

API Surface
POST   /v1/favorites/{game_id}                # favorite (idempotent; Authorization required)
DELETE /v1/favorites/{game_id}                # unfavorite
GET    /v1/users/{user_id}/favorites?limit&cursor
GET    /v1/favorites/status?user_id=U&game_ids=G1,G2,...,Gn
GET    /v1/games/{game_id}/favorites/count
GET    /v1/games/top?limit=100&window=24h|7d|all


Headers: Authorization: Bearer …, optional Idempotency-Key on POST.

Data Model (DynamoDB)

Table: favorites_by_user (authoritative)

PK = USER#<user_id>

SK = TS#<inverted_ts>#GAME#<game_id> (sort newest first)

Attributes: game_id, created_at, state ∈ {ACTIVE,REMOVED}, ver

(Optional GSI) reverse lookups: GSI1PK = GAME#<game_id>, GSI1SK = USER#<user_id>

Table: game_fav_counters (durable counters)

PK = GAME#<game_id>, attrs: count, updated_at

Redis keys

Membership: fav:has:<user_id>:<game_id> -> 0/1 (TTL 10–60s)

User first page cache: fav:list:page1:<user_id> (TTL 30–120s)

Sharded counters: fav:cnt:<game_id>:<shard_id> (N shards)

Memoized sum: fav:cnt:<game_id>:sum (TTL 2–10s)

Leaderboards (ZSET): top:all, top:24h:<bucket>, top:24h:merged, etc.

Write Path (favorite/unfavorite)

Favorite: Conditional write

Create if absent OR flip REMOVED → ACTIVE. Bump ver.

Emit FavoriteCreated(user_id, game_id, ts, ver) to stream.

INCR one random shard (fav:cnt:g:s), DEL fav:cnt:g:sum.

Invalidate membership/user-page caches.

Unfavorite: Conditional update

Set state='REMOVED' if ACTIVE.

Emit FavoriteDeleted(...).

DECR a shard, DEL fav:cnt:g:sum.

Invalidate caches.

Idempotency: accept Idempotency-Key and dedupe retries in Redis.

User Flows (Sequence Diagrams)
(1) Get all favorite games for a user (paged)
sequenceDiagram
  actor U as User
  participant App as Client App
  participant API as API Gateway
  participant S as Favorites Service
  participant R as Redis
  participant DB as DynamoDB
  participant GM as Game Metadata

  U->>App: Open "My Favorites"
  App->>API: GET /users/{uid}/favorites?limit=50&cursor=
  API->>S: Forward
  S->>R: GET fav:list:page1:uid
  alt Cache HIT
    R-->>S: list + next_cursor
  else Cache MISS
    S->>DB: Query PK=USER#uid LIMIT=50
    DB-->>S: rows (game_ids, created_at)
    S->>GM: BatchGet(game_ids)
    GM-->>S: titles, thumbnails, etc.
    S->>R: SETEX fav:list:page1:uid ...
  end
  S-->>API: list + next_cursor
  API-->>App: 200 OK

(2) Check if user has favorited a game (batch)
sequenceDiagram
  participant App
  participant API
  participant S as Favorites Service
  participant R as Redis
  participant DB as DynamoDB

  App->>API: GET /favorites/status?user_id=U&game_ids=G1..Gn
  API->>S: Forward
  S->>R: MGET fav:has:U:G1..Gn
  alt All HIT
    R-->>S: 0/1 map
  else Some MISS
    S->>DB: BatchGet (USER#U, GAME#Gi) for misses
    DB-->>S: existence map
    S->>R: SETEX missing keys (0/1)
  end
  S-->>API: {G1:true, G2:false, ...}
  API-->>App: 200 OK

(3) Get total favorite count for a game
sequenceDiagram
  participant App
  participant API
  participant S as Favorites Service
  participant R as Redis
  participant D as Durable (DDB)

  App->>API: GET /games/{G}/favorites/count
  API->>S: Forward
  S->>R: GET fav:cnt:G:sum
  alt Sum HIT
    R-->>S: total
  else Sum MISS
    S->>R: MGET fav:cnt:G:* (all shards)
    R-->>S: shard values
    S->>R: SETEX fav:cnt:G:sum (TTL few sec)
    S-->>API: computed total
    Note over S,D: If Redis degraded, fall back to DDB durable counter.
  end
  API-->>App: 200 OK (count)

(4) Get Top-K trending games / leaderboard
sequenceDiagram
  participant App
  participant API
  participant S as Favorites Service
  participant Z as Redis ZSETs
  participant W as Leaderboard Worker
  participant M as Game Metadata

  App->>API: GET /games/top?limit=100&window=24h
  API->>S: Forward
  par Background
    W->>Z: Maintain windowed buckets & merged ZSET
  end
  S->>Z: ZREVRANGE top:24h:merged 0 K-1 WITHSCORES
  Z-->>S: [(game_id, score)...]
  S->>M: BatchGet(game_ids)
  M-->>S: titles, thumbs, etc.
  S-->>API: [{game_id, score, metadata}...]
  API-->>App: 200 OK

Caching, Counters, Leaderboards

Membership

Cache 0/1 per (user, game); TTL 10–60s.

On write, delete then set to the new truth (read-your-write).

Counters

Sharded keys per game (N=16/32/64 by hotness).

Memoize sums for a few seconds.

Stream worker compacts to durable store for recovery & cold reads.

Leaderboards

All-time: refresh ZSET from durable counters (hot IDs every 10–30s; others 1–5m).

Windowed (e.g., 24h): maintain per-bucket ZSETs; merge to top:24h:merged continuously.

Consistency & SLAs

User actions & membership: read-your-write (strong for own session).

Counts/leaderboards: eventual (seconds). Show “Updated N sec ago”.

SLOs:

99.9% writes < 120ms

99% membership checks < 60ms

Cache hit ratio > 90% for counts and membership on hot content

Abuse, Privacy, and Ops

Rate limits: per user/IP/device (e.g., ≤30 favorites/min).

Fraud controls: account age thresholds, velocity checks.

GDPR delete: soft-delete edges, emit compensating events, async purge + counter reconciliation.

Key observability: event volume, counter drift vs durable, cache hit ratios, P50/P95 latencies, hot-key skew.

Failure modes: Redis degraded → DB fallback; stream lag → stale leaderboard banner; regional failover with per-region stores.

Minimal DynamoDB Conditions (illustrative)
-- Favorite (create or re-activate)
PutItem if attribute_not_exists(PK) OR state = 'REMOVED'

-- Unfavorite (only if active)
UpdateItem SET state='REMOVED', removed_at=:now
  IF attribute_exists(PK) AND state='ACTIVE'

Test Checklist

Idempotent writes under concurrent favorite/unfavorite races.

Cache invalidation on every state change.

Counter correctness under high contention & shard imbalance.

Leaderboard freshness bound (SLO) and reconciliation job.

GDPR delete flows (edge removal + counter compensation).

Backfill/recompute from event log (disaster recovery drill).
