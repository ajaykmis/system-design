# Ranking Service

Orchestrates the Spotlight feed: retrieves candidates via HNSW, assembles features, scores, and returns a ranked feed.

## System Design Concepts

- **Two-stage retrieval + ranking** — Retrieval narrows millions of items to ~100 candidates (fast, approximate); Ranking applies a richer model to score and sort (slower, precise). This is the standard pattern at Snap/TikTok/YouTube.
- **Feature assembly** — combines signals from multiple sources: ANN distance (Retrieval), content metadata (PostgreSQL), real-time engagement (Redis/Flink)
- **Scoring model** — weighted formula combining relevance, popularity, engagement rate, and freshness decay. In production this would be a trained ML model.
- **Pagination** — offset-based for simplicity; production would use cursor-based

## API

### GET /feed
Get a ranked Spotlight feed for the authenticated user.

```bash
curl "http://localhost:8092/feed?limit=10" \
  -H "X-User-ID: <user_id>"
```

**Query params**: `limit` (max 50, default 20), `offset` (default 0), `category` (filter)

**Response** `200`:
```json
{
  "items": [
    {
      "content_id": "uuid",
      "title": "Funny cat compilation",
      "category": "comedy",
      "creator_id": "uuid",
      "score": 0.547,
      "features": {
        "ann_distance": 0.23,
        "view_count_1h": 1200,
        "like_count_1h": 45,
        "engagement_rate": 0.0375
      }
    }
  ],
  "next_offset": 10
}
```

### GET /health
```bash
curl http://localhost:8092/health
```

## Running

```bash
cd snapchat/ranking
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8092
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_DSN` | `postgresql://snapuser:snappass@localhost:5433/snapchat` | PostgreSQL connection |
| `REDIS_ADDR` | `localhost:6379` | Redis for real-time features |
| `RETRIEVAL_URL` | `http://localhost:8091` | Retrieval service URL |

## Scoring Formula

```
score = 0.40 * relevance        # 1 / (1 + ann_distance)
      + 0.25 * popularity       # log(1 + view_count) / 10
      + 0.20 * engagement       # (likes + shares) / views
      + 0.15 * freshness        # exp(-0.693 * age_hours / 24)
```

## Feed Pipeline

```
User request
  → get user embedding (avg of their content, or random for new users)
  → call Retrieval /retrieve (HNSW top-100 candidates)
  → batch-fetch metadata from PostgreSQL
  → batch-fetch real-time features from Redis (pipelined)
  → score each candidate
  → sort by score descending
  → paginate and return top-N
```
