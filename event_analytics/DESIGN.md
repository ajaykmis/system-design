# Event Analytics Platform — System Design

Build an in-house event analytics system (alternative to Amplitude/Mixpanel) that ingests user behavior events from mobile/web, provides real-time dashboards, enriches events, and supports complex warehouse queries.

---

## Requirements

### Functional
1. **SDK + Ingestion API** — collect events from mobile/web clients
2. **Real-time dashboard** — per-minute event counts, <500ms p99, 1 week lookback
3. **Enrichment pipeline** — reverse-geo, GDPR compliance, context tagging
4. **Event store** — durable storage of raw + enriched events
5. **Warehouse** — complex queries (e.g., "page loads grouped by US state over 3 months")

### Non-Functional
- Dashboard queries for any minute in the last 7 days: **<500ms p99**
- Handle event bursts (product launches, viral moments)
- Events are immutable once stored (append-only)
- Enrichment is async — dashboard shows raw counts immediately

---

## Architecture

```
  Mobile/Web SDK
       │
       │ POST /v1/events (batch)
       ▼
┌──────────────┐
│ Ingestion API│ :8100 (Go)
│  validate    │
│  batch accept│
└──────┬───────┘
       │
       ├──────────────────────────────────────┐
       │                                      │
       ▼                                      ▼
┌─────────────┐                     ┌──────────────────┐
│ Kafka       │                     │ Pre-Aggregation  │
│ "raw-events"│                     │ Worker           │
└──────┬──────┘                     │ (per-minute      │
       │                            │  counters)       │
       ├────────────┐               └────────┬─────────┘
       │            │                        │
       ▼            ▼                        ▼
┌────────────┐ ┌──────────┐          ┌─────────────┐
│ Enrichment │ │ Raw Event│          │ Redis       │
│ Workers    │ │ Store    │          │ Sorted Sets │
│ (geo, gdpr,│ │ (append  │          │ per-minute  │
│  tagging)  │ │  only)   │          │ counters    │
└─────┬──────┘ └──────────┘          └──────┬──────┘
      │                                     │
      ▼                                     │
┌──────────────┐                            │
│ Enriched     │                   ┌────────┴────────┐
│ Event Store  │                   │ Dashboard API   │
│ (PostgreSQL  │                   │ :8101           │
│  partitioned)│                   │ <500ms p99      │
└──────┬───────┘                   └─────────────────┘
       │
       ▼
┌──────────────┐
│ Warehouse    │
│ (ClickHouse  │
│  or PG +     │
│  partitions) │
└──────────────┘
```

---

## Component Deep Dives

### 1. SDK + Ingestion API

**SDK (client-side):**
- Lightweight JS/mobile library that batches events locally
- Flushes every 5 seconds or when batch reaches 20 events
- Retry with exponential backoff on network failure
- Each event: `{event_name, timestamp, user_id, properties: {}}`

**Ingestion API (server-side, Go :8100):**
```
POST /v1/events
Content-Type: application/json

{
  "events": [
    {
      "event_name": "page_load",
      "timestamp": "2026-04-20T10:05:32Z",
      "user_id": "user_abc",
      "device_id": "dev_123",
      "properties": {
        "page": "/home",
        "platform": "web",
        "ip": "1.2.3.4"
      }
    }
  ]
}

Response: 202 Accepted
{"accepted": 3, "request_id": "req_xyz"}
```

Key design decisions:
- **202 Accepted** (not 200 OK) — we acknowledge receipt, not processing
- **Batch endpoint** — reduces HTTP overhead, SDK controls batching
- **Validate schema but don't block** — bad events go to a dead-letter topic
- **Publish to Kafka** immediately, return to client — sub-10ms latency

### 2. Real-Time Dashboard (the <500ms requirement)

**Problem:** "Show installs per minute over the last 2 hours" in <500ms.

**Solution: Pre-aggregated counters in Redis**

Instead of scanning raw events at query time, we pre-aggregate into per-minute buckets as events arrive.

**Redis data model:**
```
Key:    counts:{event_name}:{minute_bucket}
Type:   Sorted Set (ZINCRBY)
Member: the dimension value (e.g., "total", or a specific platform)
Score:  the count

Example:
  ZINCRBY counts:install:2026-04-20T10:05 1 "total"
  ZINCRBY counts:page_load:2026-04-20T10:05 1 "web"
  ZINCRBY counts:page_load:2026-04-20T10:05 1 "ios"
```

**Why this is fast:**
- Query "installs per minute, last 2 hours" = read 120 Redis keys (one per minute)
- Each key read is O(1) for a single member, O(K) for K members
- Redis pipelining: all 120 reads in a single round-trip
- Total: ~1-5ms, well under 500ms

**TTL:** Keys expire after 8 days (1 week + 1 day buffer).

**Pre-aggregation worker:**
- Consumes from Kafka `raw-events`
- For each event: `ZINCRBY counts:{event_name}:{minute} 1 "total"`
- Runs as a separate consumer group from enrichment (independent)

**Dashboard API (Go :8101):**
```
GET /v1/dashboard/timeseries?event=install&start=2026-04-20T08:00&end=2026-04-20T10:00&granularity=minute

Response:
{
  "event": "install",
  "granularity": "minute",
  "data": [
    {"minute": "2026-04-20T08:00", "count": 142},
    {"minute": "2026-04-20T08:01", "count": 158},
    ...
  ]
}
```

### 3. Enrichment Pipeline

**Enrichment is async** — it doesn't block ingestion or the dashboard. Raw events power the dashboard counters; enriched events go to the warehouse.

**Enrichment workers consume from Kafka `raw-events`:**

```
Raw event:
  {event: "page_load", ip: "1.2.3.4", user_id: "abc", ...}

After enrichment:
  {event: "page_load", ip: "1.2.3.4", user_id: "abc",
   geo: {country: "US", state: "CA", city: "San Francisco"},
   gdpr: {consent: true, data_region: "us-west"},
   context: {session_id: "s_123", is_new_user: false},
   enriched_at: "2026-04-20T10:05:33Z"}
```

**Enrichment types:**
| Enrichment | Source | Latency |
|-----------|--------|---------|
| Reverse-geo (IP → location) | MaxMind GeoIP local DB | <1ms |
| GDPR compliance | User consent DB lookup | ~5ms |
| Context tagging | Session store (Redis) | ~2ms |
| Device fingerprinting | User-Agent parsing | <1ms |

**Design:** Each enrichment is a function applied in sequence (pipeline pattern). Failed enrichments are logged but don't drop the event — partial enrichment is better than no data.

### 4. Event Store

Two tiers:

**Hot store (PostgreSQL, partitioned by day):**
```sql
CREATE TABLE events (
    id              UUID DEFAULT gen_random_uuid(),
    event_name      TEXT NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL,
    user_id         TEXT,
    device_id       TEXT,
    properties      JSONB,
    enrichments     JSONB,
    received_at     TIMESTAMPTZ DEFAULT NOW()
) PARTITION BY RANGE (timestamp);

-- Auto-create daily partitions
CREATE TABLE events_2026_04_20 PARTITION OF events
    FOR VALUES FROM ('2026-04-20') TO ('2026-04-21');
```

**Why partitioned by day:**
- Drop old partitions instantly (vs. DELETE which is slow)
- Queries with time ranges only scan relevant partitions
- Each partition can be independently vacuumed/indexed

**Cold store (future):** After 30 days, move to Parquet files in object storage for the warehouse.

### 5. Warehouse Queries

For complex queries like "page loads grouped by US state over 3 months":

**Option A: PostgreSQL with good partitioning + indexes**
```sql
SELECT enrichments->>'geo'->>'state' AS state, COUNT(*)
FROM events
WHERE event_name = 'page_load'
  AND timestamp >= NOW() - INTERVAL '3 months'
GROUP BY state
ORDER BY count DESC;
```

For the MVP, this works with daily partitions and a BRIN index on timestamp.

**Option B (production): ClickHouse**
- Columnar storage — only reads the columns needed
- 10-100x faster than PostgreSQL for analytical queries
- MergeTree engine with partition by month

**For our prototype:** PostgreSQL with partitioning. The design doc covers when to graduate to ClickHouse.

---

## Key Design Trade-offs

| Decision | Trade-off |
|----------|----------|
| **Pre-aggregate in Redis** vs. query raw events | Fast reads (O(1) per minute) but can't add new dimensions retroactively |
| **Async enrichment** vs. sync | Dashboard is instant but shows raw counts, not enriched |
| **202 Accepted** vs. 200 OK | Client doesn't know if event was processed, but ingestion is fast |
| **Daily partitions** vs. monthly | More partitions to manage, but faster drops and better query pruning |
| **PostgreSQL** vs. ClickHouse for warehouse | Simpler ops, but slower for large analytical queries |

---

## Capacity Estimation

Assume 10M events/day (startup scale):
- **Ingestion:** ~115 events/sec avg, ~1000/sec peak → single Go server handles this easily
- **Kafka:** 1 topic, 6 partitions, ~1KB per event → ~10GB/day
- **Redis counters:** ~1440 keys/day per event type × ~10 event types → ~14K keys/day, TTL 8 days → ~100K keys max
- **PostgreSQL:** ~10GB/day raw, ~300GB/month → need partitioning + cold storage by month 3
- **Dashboard query:** 120 Redis key reads (2 hours of minutes) → <5ms

---

## Implementation Plan

### Part 1: Ingestion + Dashboard
- Ingestion API (Go :8100) — validate, publish to Kafka
- Pre-aggregation worker (Python) — Kafka → Redis ZINCRBY
- Dashboard API (Go :8101) — Redis pipeline reads
- Dashboard UI (HTML) — simple line chart via Chart.js
- Load generator script — simulate event traffic

### Part 2: Enrichment + Store
- Enrichment worker (Python) — geo lookup, context tagging
- Event store (PostgreSQL partitioned by day)
- Warehouse queries on enriched data

### Part 3: SDK
- Lightweight JS SDK with batching, retry, and local queue

---

## How This Maps to Production (Snap's Architecture)

Snap runs two separate systems for two different problems. The event analytics design question covers **both**, and maps directly to how Snap operates at scale.

### Two Pipelines at Snap

```
PIPELINE 1: Infra Metrics ("how is the system doing?")
──────────────────────────────────────────────────────
  App container + Envoy sidecar
       │
    StatsD (per-pod process)
    ├─ collects app-emitted counters/timers
    └─ collects Envoy L7 metrics (latency, error rates, QPS)
       │
    Metrics ingestion (Carbon / M3 Aggregator)
       │
    M3DB (distributed time-series DB, replaced Graphite's Whisper)
       │
    Grafana dashboards + alerting
       │
    Query via Graphite API or PromQL (M3 Coordinator serves both)

  Example queries:
    "p99 latency of registration service, last 1 hour"
    "error rate by endpoint, last 15 minutes"

PIPELINE 2: Product/Funnel Events ("what are users doing?")
───────────────────────────────────────────────────────────
  App emits structured events (e.g., SNAP_PHONE_PAGE_SEEN, PHONE_VERIFIED)
       │
    Blizzard (internal async pipeline, Dataflow-based)
    ├─ enrichment (geo, GDPR, context)
    ├─ transformation
    └─ validation
       │
    BigQuery (columnar warehouse)
       │
    Analytics dashboards + ad-hoc SQL queries

  Example queries:
    "SNAP_PHONE_PAGE_SEEN → PHONE_VERIFIED conversion rate by country"
    "Daily active users by platform over last 90 days"
```

### Mapping to the Design Question

| Design Requirement | Snap Equivalent | Our Prototype |
|---|---|---|
| **SDK + Ingestion** | App emits events via internal SDK | Go ingestion API :8100 |
| **Async pipeline** | Blizzard (Dataflow) | Kafka → aggregator/enrichment workers |
| **Real-time dashboard** | M3DB + Grafana (infra) | Redis pre-aggregated counters + Dashboard API |
| **Enrichment** | Blizzard enrichment stages (geo, GDPR) | Enrichment worker (same pattern) |
| **Warehouse** | BigQuery | PostgreSQL partitioned (prod: ClickHouse or BigQuery) |

### Why Two Storage Systems?

The design question asks for both <500ms dashboard queries AND complex warehouse queries. These are fundamentally different access patterns:

| | Real-Time Dashboard | Warehouse |
|---|---|---|
| **Query shape** | "Count of X per minute, last 2 hours" | "Count of X grouped by state, last 3 months" |
| **Data model** | Pre-aggregated counters (time-series) | Full event records (columnar) |
| **Latency** | <5ms (Redis) / <50ms (M3DB) | 1-30 seconds (BigQuery) |
| **Storage** | M3DB or Redis (hot, TTL-based) | BigQuery or ClickHouse (cold, partitioned) |
| **Cardinality** | Low (event_name × minute) | High (event_name × user × properties) |

Trying to serve both from one system fails:
- Warehouse on Redis? Can't do "group by state" — no dimensional queries
- Dashboard on BigQuery? 2-5 second query latency, fails the <500ms requirement

This is why Snap has M3DB AND BigQuery, and why our design has Redis AND PostgreSQL/ClickHouse.

### Interview Framing

> "I've operated both halves of this at Snap. For infra metrics, we use Envoy sidecars with StatsD pushing to M3DB — that's the real-time dashboard equivalent. For product events like registration funnel metrics, we have Blizzard, an async Dataflow pipeline that enriches events and loads them into BigQuery for warehouse queries. The design I'm proposing follows the same split: pre-aggregated counters for real-time, columnar warehouse for analytics."

### Production Scale Considerations

At Snap's scale (~300M+ DAU), the key challenges beyond our prototype:

1. **Ingestion rate**: Millions of events/sec → need Kafka partitioning + multiple ingestion servers
2. **Aggregation cardinality**: Millions of unique (event, dimension) combinations → M3DB handles this with compaction and downsampling tiers
3. **Enrichment throughput**: Blizzard runs as parallel Dataflow jobs across many workers, not a single Python process
4. **Warehouse query cost**: BigQuery charges per bytes scanned → partitioning by date + clustering by event_name reduces cost 10-100x
5. **Late-arriving events**: Events from mobile devices can arrive hours late → watermarking and late-data handling in the pipeline
6. **Schema evolution**: New event properties added frequently → BigQuery's schema auto-detection + Blizzard's flexible transformation stages
