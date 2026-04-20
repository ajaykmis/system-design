# Ingestion Service

Accepts content uploads and engagement events. Generates embeddings, stores metadata in PostgreSQL, and publishes events to Kafka for downstream processing.

## System Design Concepts

- **Pub/Sub** — content creation events published to Kafka `content-raw` topic, consumed by the Retrieval service for index building
- **Event streaming** — engagement events (views, likes, shares) published to `engagement-events` topic, consumed by Flink for real-time feature aggregation
- **Embedding generation** — mock 128-dim vectors with category-based clustering. Same-category content has similar embeddings, making HNSW retrieval meaningful
- **Decoupled ingestion** — write path (store + publish) is independent of read path (retrieval + ranking)

## API

### POST /content
Upload new content. Requires auth (X-User-ID header set by Gateway).

```bash
curl -X POST http://localhost:8090/content \
  -H "Content-Type: application/json" \
  -H "X-User-ID: <user_id>" \
  -d '{"title": "Funny cat video", "description": "Cat falls off table", "category": "comedy"}'
```

**Response** `200`:
```json
{"content_id": "uuid"}
```

**Categories**: comedy, sports, music, food, travel, fashion, tech, pets, dance, diy

### POST /events
Record an engagement event (view, like, share).

```bash
curl -X POST http://localhost:8090/events \
  -H "Content-Type: application/json" \
  -H "X-User-ID: <user_id>" \
  -d '{"content_id": "uuid", "event_type": "view"}'
```

### GET /health
```bash
curl http://localhost:8090/health
```

## Running

```bash
cd snapchat/ingestion
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8090
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_DSN` | `postgresql://snapuser:snappass@localhost:5433/snapchat` | PostgreSQL connection |
| `KAFKA_BOOTSTRAP` | `localhost:9092` | Kafka broker address |

## Kafka Topics

| Topic | Key | Consumers |
|-------|-----|-----------|
| `content-raw` | `content_id` | Retrieval service (index builder) |
| `engagement-events` | `content_id` | Flink feature pipeline |

## Embedding Strategy

The mock embedder produces 128-dim vectors that cluster by category:
- **70% weight** from a fixed category centroid (each category has a unique direction)
- **30% weight** from a text-specific hash vector (unique per content)

This means HNSW queries with a "comedy" embedding will return comedy content first — making the retrieval pipeline behave realistically without a real ML model.
