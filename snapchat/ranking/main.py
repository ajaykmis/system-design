"""Ranking Service — assembles features, scores candidates, serves the Spotlight feed."""

import logging
import os
import struct
import time
from contextlib import asynccontextmanager

import psycopg2
import psycopg2.extras
import redis
import requests
from fastapi import FastAPI, Header, HTTPException, Query
from pydantic import BaseModel

from circuit_breaker import CircuitBreaker
from ranker import rank_candidates

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

# --- Config ---
POSTGRES_DSN = os.getenv("POSTGRES_DSN", "postgresql://snapuser:snappass@localhost:5433/snapchat")
REDIS_ADDR = os.getenv("REDIS_ADDR", "localhost:6380")
RETRIEVAL_URL = os.getenv("RETRIEVAL_URL", "http://localhost:8091")
DIM = 128

# --- Globals ---
db_conn = None
redis_client = None
retrieval_breaker = CircuitBreaker("retrieval", failure_threshold=3, cooldown_seconds=15)


def get_db():
    global db_conn
    if db_conn is None or db_conn.closed:
        db_conn = psycopg2.connect(POSTGRES_DSN)
        db_conn.autocommit = True
    return db_conn


def get_redis():
    global redis_client
    if redis_client is None:
        host, port = REDIS_ADDR.split(":")
        redis_client = redis.Redis(host=host, port=int(port), decode_responses=True)
    return redis_client


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("Ranking service starting")
    get_db()
    get_redis()
    yield
    if db_conn and not db_conn.closed:
        db_conn.close()
    logger.info("Ranking service stopped")


app = FastAPI(title="Ranking Service", lifespan=lifespan)


# --- Models ---

class FeedItem(BaseModel):
    content_id: str
    title: str
    category: str
    creator_id: str
    score: float
    features: dict


class FeedResponse(BaseModel):
    items: list[FeedItem]
    next_offset: int


# --- Helpers ---

def bytes_to_embedding(data: bytes) -> list[float]:
    n = len(data) // 4
    return list(struct.unpack(f"{n}f", data))


def get_user_embedding(user_id: str) -> list[float] | None:
    """Get a user interest embedding.

    For the MVP, we use the average embedding of the user's own content
    as a proxy for their interests. In production this would come from
    a user embedding model trained on engagement history.
    """
    conn = get_db()
    with conn.cursor() as cur:
        cur.execute(
            "SELECT embedding FROM content WHERE creator_id = %s AND embedding IS NOT NULL LIMIT 10",
            (user_id,),
        )
        rows = cur.fetchall()

    if not rows:
        return None

    import numpy as np
    embeddings = [bytes_to_embedding(row[0]) for row in rows]
    avg = np.mean(embeddings, axis=0).astype("float32")
    avg /= np.linalg.norm(avg)
    return avg.tolist()


def _call_retrieval(query_embedding: list[float], top_k: int) -> list[dict]:
    """Direct call to the Retrieval service."""
    resp = requests.post(
        f"{RETRIEVAL_URL}/retrieve",
        json={"query_embedding": query_embedding, "top_k": top_k},
        timeout=5,
    )
    resp.raise_for_status()
    return resp.json()["candidates"]


def fetch_candidates(query_embedding: list[float], top_k: int = 100) -> list[dict]:
    """Call the Retrieval service for ANN candidates, protected by circuit breaker.

    If the retrieval service is down (circuit open), returns an empty list
    so the feed degrades gracefully instead of timing out.
    """
    return retrieval_breaker.call(
        fn=lambda: _call_retrieval(query_embedding, top_k),
        fallback=lambda: [],
    )


def fetch_content_metadata(content_ids: list[str]) -> dict[str, dict]:
    """Batch-fetch content metadata from PostgreSQL."""
    if not content_ids:
        return {}

    conn = get_db()
    with conn.cursor(cursor_factory=psycopg2.extras.DictCursor) as cur:
        cur.execute(
            "SELECT id, title, category, creator_id, created_at FROM content WHERE id = ANY(%s::uuid[])",
            (content_ids,),
        )
        rows = cur.fetchall()

    result = {}
    now = time.time()
    for row in rows:
        cid = str(row["id"])
        created_ts = row["created_at"].timestamp()
        age_hours = (now - created_ts) / 3600.0
        result[cid] = {
            "title": row["title"],
            "category": row["category"],
            "creator_id": str(row["creator_id"]),
            "age_hours": age_hours,
        }
    return result


def fetch_realtime_features(content_ids: list[str]) -> dict[str, dict]:
    """Fetch real-time engagement features from Redis (written by Flink pipeline)."""
    r = get_redis()
    result = {}
    pipe = r.pipeline()
    for cid in content_ids:
        pipe.hgetall(f"features:{cid}")
    responses = pipe.execute()

    for cid, data in zip(content_ids, responses):
        result[cid] = {
            "view_count_1h": int(data.get("view_count_1h", 0)),
            "like_count_1h": int(data.get("like_count_1h", 0)),
            "share_count_1h": int(data.get("share_count_1h", 0)),
        }
    return result


# --- Endpoints ---

@app.get("/feed", response_model=FeedResponse)
def get_feed(
    limit: int = Query(default=20, le=50),
    offset: int = Query(default=0),
    category: str = Query(default=""),
    x_user_id: str = Header(None),
):
    if not x_user_id:
        raise HTTPException(status_code=401, detail="missing user context")

    # 1. Get user embedding (proxy for interests)
    user_embedding = get_user_embedding(x_user_id)
    if user_embedding is None:
        # New user with no content — use a random-ish default
        import numpy as np
        rng = np.random.RandomState(hash(x_user_id) % 2**32)
        user_embedding = rng.randn(DIM).astype("float32")
        user_embedding /= np.linalg.norm(user_embedding)
        user_embedding = user_embedding.tolist()

    # 2. Retrieve candidates via HNSW
    candidates = fetch_candidates(user_embedding, top_k=100)
    if not candidates:
        return FeedResponse(items=[], next_offset=0)

    content_ids = [c["content_id"] for c in candidates]

    # 3. Fetch metadata + real-time features
    metadata = fetch_content_metadata(content_ids)
    features = fetch_realtime_features(content_ids)

    # 4. Assemble feature vectors for scoring
    enriched = []
    for c in candidates:
        cid = c["content_id"]
        meta = metadata.get(cid, {})
        feat = features.get(cid, {})

        if category and meta.get("category", "") != category:
            continue

        enriched.append({
            "content_id": cid,
            "distance": c["distance"],
            "title": meta.get("title", ""),
            "category": meta.get("category", ""),
            "creator_id": meta.get("creator_id", ""),
            "age_hours": meta.get("age_hours", 0),
            "view_count_1h": feat.get("view_count_1h", 0),
            "like_count_1h": feat.get("like_count_1h", 0),
            "share_count_1h": feat.get("share_count_1h", 0),
        })

    # 5. Score and rank
    ranked = rank_candidates(enriched)

    # 6. Paginate
    page = ranked[offset:offset + limit]
    items = [
        FeedItem(
            content_id=c["content_id"],
            title=c["title"],
            category=c["category"],
            creator_id=c["creator_id"],
            score=c["score"],
            features={
                "ann_distance": round(c["distance"], 4),
                "view_count_1h": c["view_count_1h"],
                "like_count_1h": c["like_count_1h"],
                "engagement_rate": round(
                    (c["like_count_1h"] + c["share_count_1h"]) / max(c["view_count_1h"], 1), 4
                ),
            },
        )
        for c in page
    ]

    return FeedResponse(items=items, next_offset=offset + limit)


@app.get("/debug/circuit")
def debug_circuit():
    return retrieval_breaker.status()


@app.get("/health")
def health():
    return {"status": "ok"}
