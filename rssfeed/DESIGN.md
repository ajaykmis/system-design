# RSS News Aggregator

A Google News-style aggregator that polls RSS/Atom feeds, stores articles in PostgreSQL, and serves them via a JSON API.

---

## Current Architecture

```
Browser → Go Server (:8081)
               ├── GET /api/news   → SELECT articles JOIN publishers ORDER BY published_at DESC LIMIT 200
               ├── POST /api/refresh → triggers manual re-fetch
               └── background ticker (15 min) → goroutine per publisher → HTTP GET → INSERT ON CONFLICT DO NOTHING

PostgreSQL (:5433)
  publishers  (id, name, feed_url, category, last_fetched_at)
  articles    (id, publisher_id, title, link UNIQUE, description, published_at)
```

`link UNIQUE` + `ON CONFLICT DO NOTHING` makes repeated fetches idempotent. Supports RSS 2.0 and Atom 1.0.

---

## Scale Issues (at 100M users)

| Area | Problem |
|---|---|
| Read path | Every request hits DB directly — 1% concurrency = 1M simultaneous queries |
| Feed fetching | Single server, no coordination — horizontal scaling causes every server to fetch every feed |
| Storage | No partitioning or TTL — table degrades within weeks at 10k+ publishers |
| Resiliency | Server crash = feeds go dark; no retry or backoff |
| API | Flat LIMIT 200, no pagination; static files served from app server |

---

## Improvements

**Tier 1 — Cache (10k → 1M users)**
Add Redis in front of the DB query with a 5-minute TTL. The result set changes at most every 15 minutes, so a cache absorbs ~99% of read traffic. Invalidate the cache key after each successful refresh cycle, not per-article insert.

**Tier 2 — Publisher Locks (1M → 10M users)**
When running multiple fetcher instances, use a Redis `SETNX lock:publisher:{id}` with a 30s TTL before each fetch. The lock is per-publisher (not per-article) because a feed fetch is one atomic unit of work — article deduplication is already handled by `ON CONFLICT`. If a server crashes mid-fetch the TTL auto-expires and another server picks it up.

**Tier 3 — Streaming Pipeline (10M → 100M users)**
Separate the scheduler, fetcher, and ingest into independent services connected by Kafka. The scheduler emits `FetchJob` events (still using Redis locks to avoid duplicate jobs), fetcher workers parse feeds and emit `ArticleEvent` per item, and ingest workers write to DB and invalidate cache. This decouples fetch latency from DB write latency and allows each layer to scale independently.

**Tier 4 — Storage**
Partition the articles table by month. Old partitions detach and archive to cold storage without locking. Add PgBouncer for connection pooling. For full-text search beyond ~10M rows, sync to Elasticsearch via Debezium CDC on the Postgres WAL.

**Tier 5 — Read Path**
Serve static assets and the news feed from a CDN. Replace `LIMIT 200` with cursor-based pagination (`WHERE published_at < $cursor`) to avoid the `OFFSET` performance cliff. All API reads go to a read replica; primary handles writes only.

---

## Target Architecture (100M users)

```
  External RSS/Atom Feeds
          │
          │  HTTP GET (per publisher)
          ▼
  ┌───────────────┐   SETNX lock:publisher:{id}   ┌───────┐
  │   Scheduler   │ ─────────────────────────────► │ Redis │
  │  (CronJob)    │ ◄───────────────────────────── │       │◄── cache GET/SET (news:feed:*)
  └───────┬───────┘   lock acquired → emit job     └───────┘
          │                                             ▲
          │ FetchJob{publisher_id, feed_url}            │ DEL cache on ingest
          ▼                                             │
  ┌───────────────┐                                     │
  │    Kafka      │  topic: feed.fetch.jobs             │
  └───────┬───────┘                                     │
          │                                             │
          ▼                                             │
  ┌───────────────┐                                     │
  │   Fetcher     │  GET feed_url → parse RSS/Atom      │
  │   Workers     │                                     │
  └───────┬───────┘                                     │
          │ ArticleEvent{title, link, description, ...} │
          ▼                                             │
  ┌───────────────┐                                     │
  │    Kafka      │  topic: feed.articles.raw           │
  └───────┬───────┘                                     │
          │                                             │
          ▼                                             │
  ┌───────────────┐   INSERT ON CONFLICT DO NOTHING  ┌──┴────────────────┐
  │    Ingest     │ ───────────────────────────────► │   PostgreSQL      │
  │    Workers    │                                  │   (primary)       │
  └───────────────┘                                  │                   │
                                                     │   articles        │
                                                     │   publishers      │
                                                     └──────────┬────────┘
                                                                │ streaming replication
                                                                ▼
  ┌─────────┐    ┌──────────────┐    ┌────────────┐   ┌────────────────┐
  │ Client  │───►│     CDN      │───►│    Load    │──►│  API Servers   │
  │(Browser)│    │(static+cache)│    │  Balancer  │   │  (stateless)   │
  └─────────┘    └──────────────┘    └────────────┘   └───────┬────────┘
                                                              │
                                           ┌──────────────────┼────────────────┐
                                           ▼                  ▼                │
                                      ┌────────┐   ┌──────────────────┐        │
                                      │ Redis  │   │   PostgreSQL     │        │
                                      │  L1    │   │  read replica    │        │
                                      │ 5m TTL │   │      L2          │        │
                                      └────────┘   └──────────────────┘        │
                                                                                │
                                                         Debezium CDC (WAL) ───►│
                                                                                ▼
                                                                      ┌──────────────────┐
                                                                      │  Elasticsearch   │
                                                                      │  (full-text      │
                                                                      │   search)        │
                                                                      └──────────────────┘
```

| Component | Role | Scales by |
|---|---|---|
| Scheduler | Emits FetchJobs, holds publisher locks | Single instance + Redis SETNX |
| Fetcher Workers | HTTP GET + parse RSS/Atom | Horizontal, stateless |
| Ingest Workers | Write to DB, invalidate cache | Horizontal, stateless |
| Kafka | Decouple fetch from write, replay | Partition by publisher_id |
| PostgreSQL | Source of truth | Primary + read replicas, monthly partitions |
| Redis | Publisher locks + API cache | Cluster mode |
| Elasticsearch | Full-text search (>10M rows) | Sharded, fed via Debezium CDC |
| CDN | Static assets + edge cache | Transparent |
