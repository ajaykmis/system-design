# RSS News Aggregator — Design

A Google News-style aggregator that polls RSS/Atom feeds from multiple publishers,
stores articles in PostgreSQL, and serves them through a JSON API + HTML frontend.

---

## Current Implementation (Prototype)

### Architecture

```
Browser
  └── GET /              → index.html (served by Go)
  └── GET /api/news      → JSON array of articles
  └── POST /api/refresh  → triggers manual re-fetch

Go Server (single instance, :8081)
  ├── HTTP handlers (newsHandler, refreshHandler)
  ├── Background ticker (every 15 min → refreshAll)
  └── refreshAll: goroutine per publisher → fetchAndStore

PostgreSQL (Docker, :5433)
  ├── publishers  (6 rows — seeded at startup)
  └── articles    (grows each refresh cycle)
```

### Schema

```sql
CREATE TABLE publishers (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    feed_url        TEXT UNIQUE NOT NULL,   -- RSS/Atom endpoint
    website         TEXT,
    category        TEXT,                   -- "tech" | "world"
    last_fetched_at TIMESTAMPTZ
);

CREATE TABLE articles (
    id              SERIAL PRIMARY KEY,
    publisher_id    INTEGER REFERENCES publishers(id),
    title           TEXT,
    link            TEXT UNIQUE NOT NULL,   -- deduplication key
    description     TEXT,
    pub_date        TEXT,                   -- raw string from feed
    published_at    BIGINT,                 -- unix ts, used for ORDER BY
    fetched_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX articles_published_at_idx ON articles(published_at DESC);
CREATE INDEX articles_publisher_idx    ON articles(publisher_id);
```

`link UNIQUE` is the idempotency guarantee — re-fetching uses
`INSERT ... ON CONFLICT (link) DO NOTHING`, so duplicate runs are safe.

### Feed Parsing

Supports RSS 2.0 and Atom 1.0. Parser tries RSS first, falls back to Atom.
Date strings are normalised to Unix timestamps across 6 known layouts.

```go
// parseFeed tries RSS first, then Atom
func parseFeed(body []byte) ([]parsedItem, error) {
    var rss RSS
    if err := xml.Unmarshal(body, &rss); err == nil && len(rss.Channel.Items) > 0 {
        var items []parsedItem
        for _, i := range rss.Channel.Items {
            items = append(items, parsedItem{i.Title, i.Link, i.Description, i.PubDate})
        }
        return items, nil
    }
    var atom AtomFeed
    if err := xml.Unmarshal(body, &atom); err == nil && len(atom.Entries) > 0 {
        var items []parsedItem
        for _, e := range atom.Entries {
            items = append(items, parsedItem{e.Title, e.link(), e.summary(), e.Updated})
        }
        return items, nil
    }
    return nil, fmt.Errorf("unrecognised feed format")
}
```

### Write Path

```go
// refreshAll loads all publishers and fans out one goroutine per feed
func refreshAll() {
    rows, _ := db.Query(`SELECT id, name, feed_url FROM publishers`)
    // ... scan publishers ...
    var wg sync.WaitGroup
    for _, p := range publishers {
        wg.Add(1)
        go func(pub Publisher) {
            defer wg.Done()
            fetchAndStore(pub)   // GET → parse → INSERT ON CONFLICT DO NOTHING
        }(p)
    }
    wg.Wait()
}
```

Every article insert:
```go
db.Exec(`
    INSERT INTO articles (publisher_id, title, link, description, pub_date, published_at)
    VALUES ($1, $2, $3, $4, $5, $6)
    ON CONFLICT (link) DO NOTHING`,
    pub.ID, item.title, item.link, item.description, item.pubDate, ts,
)
```

### Read Path

```go
// newsHandler — every request hits DB directly (no cache yet)
db.Query(`
    SELECT a.title, a.link, a.description, a.pub_date, p.name, a.published_at
    FROM articles a
    JOIN publishers p ON a.publisher_id = p.id
    ORDER BY a.published_at DESC
    LIMIT 200`)
```

### Known Limitations (prototype)

| Area | Issue |
|---|---|
| Single server | No horizontal scaling; one crash = no fetches |
| No cache | Every `/api/news` hits DB directly |
| No distributed lock | Multiple instances double-fetch every feed |
| Fixed 15-min interval | Ignores per-feed update frequency |
| No retry/backoff | Transient failures drop the feed silently |
| Unbounded articles table | No TTL, partitioning, or archival |
| Credentials in code | DSN hardcoded; no env config |
| No pagination | Returns 200 articles flat |
| Static files from Go | index.html served by app server, not CDN |
| No rate limit on /api/refresh | Unauthenticated, trivially abusable |

---

## Scale Design: Path to 100M Users

### Where the prototype breaks first

At meaningful scale the bottleneck ordering is:

1. **DB read path** — every request hits `articles JOIN publishers ORDER BY`.
   At 1% concurrency on 100M users = 1M simultaneous queries. PostgreSQL collapses.

2. **Feed fetcher** — single goroutine set on one server. At 10k+ publishers
   the 15-min window is too tight even with goroutines.

3. **Storage** — 10k publishers × 30 articles × 96 refreshes/day = ~29M rows/day.
   A single unpartitioned table degrades within weeks.

---

### Tier 1: Caching (10k → 1M users)

The articles result set changes at most every 15 minutes. Cache it.

```
GET /api/news
  → Redis GET news:feed:global   (TTL 5 min)
  → HIT  → return cached JSON
  → MISS → query DB → SET cache → return
```

Replace `newsHandler` with:

```go
func newsHandler(w http.ResponseWriter, r *http.Request) {
    const cacheKey = "news:feed:global"

    if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil {
        w.Header().Set("Content-Type", "application/json")
        w.Write(cached)
        return
    }

    // cache miss — query DB
    articles := queryArticles()
    payload, _ := json.Marshal(articles)
    rdb.Set(ctx, cacheKey, payload, 5*time.Minute)

    w.Header().Set("Content-Type", "application/json")
    w.Write(payload)
}
```

Cache invalidation: the fetcher calls `rdb.Del(ctx, "news:feed:global")` after
a successful refresh cycle — not per article insert (too noisy).

Cache segmented by category: `news:feed:tech`, `news:feed:world`.
This alone absorbs ~99% of read traffic.

---

### Tier 2: Distributed Feed Fetching with Publisher Locks (1M → 10M users)

When the fetcher fleet scales to multiple servers, all instances fetch all feeds
simultaneously — redundant work and external rate-limit risk.

**Solution: Redis lock per publisher, TTL = 2× expected fetch time.**

```
For each publisher p:
  SETNX lock:publisher:{p.id}  "server-{hostname}"  EX 30
    → OK   → this server owns the fetch, proceed
    → FAIL → another server has it, skip
```

Replace the goroutine body in `refreshAll`:

```go
for _, p := range publishers {
    wg.Add(1)
    go func(pub Publisher) {
        defer wg.Done()

        lockKey := fmt.Sprintf("lock:publisher:%d", pub.ID)
        // NX = only set if not exists, EX 30 = auto-expire after 30s
        ok, err := rdb.SetNX(ctx, lockKey, hostname, 30*time.Second).Result()
        if err != nil || !ok {
            return // another server owns this feed
        }
        defer rdb.Del(ctx, lockKey) // explicit early release on success

        if err := fetchAndStore(pub); err != nil {
            log.Printf("warning: %v", err)
        }
    }(p)
}
```

**Why lock per publisher and not per article:**
A feed fetch is one HTTP request → N articles inserted as a batch. Locking at
the publisher level = one lock acquisition per work unit. Locking per article
= 20–30 Redis round trips per feed — `ON CONFLICT DO NOTHING` already handles
article-level deduplication for free.

**Failure modes:**

| Scenario | Outcome |
|---|---|
| Server crashes holding lock | Lock expires after TTL; next server picks up |
| Two servers race on SETNX | One wins, one skips — no duplicate fetch |
| Redis is down | Fall back to fetch-without-lock (safe due to `ON CONFLICT`) |
| Feed URL is slow (>TTL) | Another server starts duplicate fetch; `ON CONFLICT` absorbs it |

---

### Tier 3: Decoupled Fetch Pipeline with Streaming (10M → 100M users)

At this scale the fetcher and API server should be separate services with a
message queue between them.

```
┌──────────────────────────────────────────────────────────────────┐
│ Scheduler (cron / k8s CronJob)                                   │
│   every N min: for each publisher due:                           │
│     SETNX lock:publisher:{id} EX {interval}                      │
│     → emit FetchJob{publisher_id, feed_url} to Kafka             │
│   topic: feed.fetch.jobs                                         │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                    Kafka topic
                 feed.fetch.jobs
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ Fetcher Workers (stateless, horizontally scaled)                 │
│   consume FetchJob                                               │
│   GET feed_url → parse RSS/Atom → emit ArticleEvent per item     │
│   topic: feed.articles.raw                                       │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                    Kafka topic
                 feed.articles.raw
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ Ingest Workers                                                   │
│   consume ArticleEvent                                           │
│   INSERT ... ON CONFLICT (link) DO NOTHING                       │
│   DEL Redis cache keys for affected category                     │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                 PostgreSQL (primary + read replicas)
```

**Why Kafka between fetcher and ingest:**
- DB outage doesn't stall fetchers — events buffer in the topic
- Ingest workers consume at their own pace (backpressure)
- Replay raw events if an ingest bug is found
- Fetcher and ingest scale independently

**Lock interaction with Kafka scheduler:**
The scheduler acquires the publisher lock before emitting a `FetchJob`, not the
fetcher worker. The lock TTL equals the desired fetch interval and is never
manually deleted — it auto-expires when the next cycle is due.

```go
// Scheduler (not fetcher) holds the lock
lockKey := fmt.Sprintf("lock:publisher:%d", pub.ID)
ok, _ := rdb.SetNX(ctx, lockKey, schedulerID, fetchInterval).Result()
if ok {
    kafka.Emit("feed.fetch.jobs", FetchJob{PublisherID: pub.ID, FeedURL: pub.FeedURL})
}
```

---

### Tier 4: Storage at Scale

**Partitioning by month:**

```sql
CREATE TABLE articles (
    id           BIGSERIAL,
    publisher_id INTEGER,
    title        TEXT,
    link         TEXT NOT NULL,
    published_at BIGINT NOT NULL,
    ...
) PARTITION BY RANGE (published_at);

CREATE TABLE articles_2026_04 PARTITION OF articles
    FOR VALUES FROM (1743465600) TO (1746057600);
```

Old partitions detach and archive to S3 without locking the main table.
Queries with a time-range filter only touch relevant partitions.

**Connection pooling:** PgBouncer in front of primary (writes) and replicas
(reads). API servers connect to PgBouncer, not directly to Postgres.

**Full-text search:** Postgres `tsvector` works to ~10M rows. Beyond that,
sync via Debezium CDC (reads Postgres WAL) → Kafka → Elasticsearch sink.

```
articles WAL → Debezium → Kafka topic db.public.articles
                                    → Elasticsearch sink
GET /api/search?q=...  →  ES query
```

---

### Tier 5: Read Path at Scale

```
Client
  └── CDN  (static assets + edge-cached /api/news, short TTL)
       └── Load Balancer
            └── API Servers (stateless, N instances)
                 └── Redis  (L1 cache, 5-min TTL, per-category keys)
                      └── PostgreSQL read replica  (L2)
```

**Cursor-based pagination** replaces flat LIMIT 200:

```go
// GET /api/news?cursor=1777138015&limit=20
func newsHandler(w http.ResponseWriter, r *http.Request) {
    cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
    if cursor == 0 {
        cursor = time.Now().Unix()
    }
    db.Query(`
        SELECT a.title, a.link, a.description, a.pub_date, p.name, a.published_at
        FROM articles a JOIN publishers p ON a.publisher_id = p.id
        WHERE a.published_at < $1
        ORDER BY a.published_at DESC
        LIMIT 20`, cursor)
    // response includes next_cursor = last article's published_at
}
```

Cursor-based avoids the `OFFSET` performance cliff on large tables.

---

## Component Responsibility Summary

| Component | Responsibility | Scales by |
|---|---|---|
| Scheduler | Emit FetchJobs on interval, acquire publisher lock | Single instance + Redis lock |
| Fetcher Workers | HTTP GET feed, parse RSS/Atom, emit ArticleEvents | Horizontal (stateless) |
| Ingest Workers | Deduplicate, write to DB, invalidate cache | Horizontal (stateless) |
| Kafka | Decouple fetchers from writers, buffering, replay | Partition by publisher_id |
| PostgreSQL | Source of truth for articles + publishers | Read replicas + monthly partitions |
| Redis | Publisher locks + API response cache | Cluster mode |
| API Servers | Serve /api/news cache-first | Horizontal (stateless) |
| CDN | Static assets + edge caching for news feed | Transparent |
| Elasticsearch | Full-text search | Sharded index |

---

## Code Reference

| Design concept | Code location |
|---|---|
| RSS + Atom parsing | `parseFeed()` — tries RSS, falls back to Atom |
| Idempotent inserts | `fetchAndStore()` — `INSERT ... ON CONFLICT (link) DO NOTHING` |
| Fan-out fetching | `refreshAll()` — `sync.WaitGroup` + one goroutine per publisher |
| Read path | `newsHandler()` — JOIN query, ORDER BY published_at DESC, LIMIT 200 |
| Refresh trigger | `refreshHandler()` — `go refreshAll()` async |
| 15-min ticker | `main()` — `time.NewTicker(15 * time.Minute)` |
| DB schema | PostgreSQL Docker :5433, database `rssfeed` |
| Next step | Add Redis: Tier 1 cache in `newsHandler`, Tier 2 lock in `refreshAll` |
