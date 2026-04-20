# Retrieval Service

HNSW-based approximate nearest neighbor search for Spotlight content discovery. Consumes content events from Kafka, builds an HNSW index, and serves candidate retrieval queries.

## System Design Concepts

- **HNSW (Hierarchical Navigable Small World)** — graph-based ANN index with `M=16`, `ef_construction=200`, `ef_search=50`. Better recall than IVF at the same latency, supports incremental inserts
- **Consistent hashing** — hash ring maps content_id to shard. MVP uses a single shard; the ring is exposed via debug endpoint to demonstrate distribution
- **Kafka consumer** — background thread consumes `content-raw` events and incrementally adds to the HNSW index
- **Bootstrap from DB** — on startup, loads all existing content embeddings from PostgreSQL before consuming Kafka

## API

### POST /retrieve
Find nearest neighbors for a query embedding.

```bash
curl -X POST http://localhost:8091/retrieve \
  -H "Content-Type: application/json" \
  -d '{"query_embedding": [0.1, 0.2, ...], "top_k": 20}'
```

**Response** `200`:
```json
{
  "candidates": [
    {"content_id": "uuid", "distance": 0.23},
    {"content_id": "uuid", "distance": 0.45}
  ]
}
```

### GET /debug/ring
Show consistent hash ring state.

```bash
curl http://localhost:8091/debug/ring
```

```json
{"nodes": ["retrieval-1"], "vnodes_per_node": 150, "total_vnodes": 150}
```

### GET /debug/index
Show HNSW index stats.

```bash
curl http://localhost:8091/debug/index
```

```json
{"total_items": 1000, "max_elements": 100000, "ef_search": 50, "M": 16, "space": "l2", "dim": 128}
```

### GET /health
```bash
curl http://localhost:8091/health
# {"status": "ok", "index_size": 1000}
```

## Running

```bash
cd snapchat/retrieval
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8091
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_DSN` | `postgresql://snapuser:snappass@localhost:5433/snapchat` | PostgreSQL connection |
| `KAFKA_BOOTSTRAP` | `localhost:29092` | Kafka broker address |
| `NODE_ID` | `retrieval-1` | This node's ID in the hash ring |
| `INDEX_DIR` | `/tmp/snap-hnsw-index` | Directory for serialized index files |

## HNSW Parameters

| Parameter | Value | Effect |
|-----------|-------|--------|
| `M` | 16 | Connections per node per layer. Higher = better recall, more memory |
| `ef_construction` | 200 | Build-time beam width. Higher = better index quality, slower builds |
| `ef_search` | 50 | Query-time beam width. Higher = better recall, slower queries |
| `space` | L2 | Distance metric (Euclidean) |
| `dim` | 128 | Embedding dimensionality |

## Architecture

```
Startup: PostgreSQL → load embeddings → build initial HNSW index
Runtime: Kafka consumer → add new items → incremental index updates
Queries: POST /retrieve → HNSW knn_query → ranked candidates
```
