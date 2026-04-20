# Feature Pipeline

Stream processor that consumes engagement events from Kafka, computes windowed aggregations, and materializes features to Redis for the Ranking service.

## System Design Concepts

- **Stream processing** — continuous consumption and transformation of an event stream, same pattern as a Flink/Spark Streaming job
- **Tumbling windows** (1 min) — fixed, non-overlapping time windows. Each minute's events are counted independently, then the counter resets
- **Sliding windows** (1 hour) — events within a rolling 1-hour range. Uses a deque with eviction of expired entries, mirroring Flink's internal ring buffer approach
- **Materialized views** — stream aggregates are periodically flushed to Redis, creating a queryable view of the stream state. The Ranking service reads these without knowing about Kafka

## Architecture

```
Kafka (engagement-events)
  → Consumer (poll loop)
  → keyBy(content_id)
  → TumblingWindow(60s)  — per-minute counters
  → SlidingWindow(3600s) — rolling 1-hour aggregates
  → Redis sink (every 5s) — materialized features
```

### Flink Comparison

| Aspect | This Implementation | Production Flink |
|--------|-------------------|-----------------|
| Parallelism | Single thread | Parallel operators across TaskManagers |
| Fault tolerance | None (restart from earliest) | Checkpointing + exactly-once |
| Windowing | Manual deque eviction | Built-in window operators |
| State backend | In-memory dict | RocksDB / heap state backend |
| Sink | Redis pipeline | Configurable (Redis, Kafka, DB) |

The semantics are identical — the difference is operational: Flink handles scale, failures, and backpressure automatically.

## Running

```bash
cd snapchat/feature_pipeline
pip install -r requirements.txt
python main.py
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KAFKA_BOOTSTRAP` | `localhost:29092` | Kafka broker address |
| `REDIS_ADDR` | `localhost:6380` | Redis for feature materialization |

## Redis Output

The pipeline writes to Redis hashes keyed by `features:{content_id}`:

```
HGETALL features:<content_id>
→ view_count_1h: 1500
  like_count_1h: 75
  share_count_1h: 20
  engagement_rate: 0.0633
```

Keys expire after 2 hours if no new events arrive.

## Testing

```bash
# 1. Start the pipeline
python main.py

# 2. Send engagement events via Ingestion API
curl -X POST http://localhost:8090/events \
  -H "Content-Type: application/json" \
  -H "X-User-ID: <user_id>" \
  -d '{"content_id": "<id>", "event_type": "view"}'

# 3. Wait 5 seconds (flush interval), then check Redis
redis-cli -p 6380 HGETALL "features:<content_id>"

# 4. Check the feed — features should affect ranking
curl "http://localhost:8080/api/v1/feed?limit=5" -H "Authorization: Bearer <token>"
```
