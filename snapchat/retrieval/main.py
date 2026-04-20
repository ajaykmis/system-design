"""Retrieval Service — HNSW-based ANN search with Kafka consumer for index building."""

import json
import logging
import os
import struct
import threading
from contextlib import asynccontextmanager

import psycopg2
from confluent_kafka import Consumer, KafkaError
from fastapi import FastAPI
from pydantic import BaseModel

from consistent_hash import ConsistentHashRing
from index import HNSWIndex

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

# --- Config ---
POSTGRES_DSN = os.getenv("POSTGRES_DSN", "postgresql://snapuser:snappass@localhost:5433/snapchat")
KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "localhost:29092")
NODE_ID = os.getenv("NODE_ID", "retrieval-1")
CONTENT_TOPIC = "content-raw"
DIM = 128

# --- Globals ---
hnsw_index = HNSWIndex(dim=DIM)
hash_ring = ConsistentHashRing(nodes=[NODE_ID])
consumer_thread = None
running = True


def bytes_to_embedding(data: bytes) -> list[float]:
    """Unpack BYTEA from PostgreSQL into float list."""
    n = len(data) // 4
    return list(struct.unpack(f"{n}f", data))


def load_existing_content():
    """Bootstrap: load all existing content from PostgreSQL into the HNSW index."""
    try:
        conn = psycopg2.connect(POSTGRES_DSN)
        with conn.cursor() as cur:
            cur.execute("SELECT id, embedding FROM content WHERE embedding IS NOT NULL")
            rows = cur.fetchall()
        conn.close()

        if not rows:
            logger.info("No existing content to index")
            return

        content_ids = []
        embeddings = []
        for row in rows:
            content_ids.append(str(row[0]))
            embeddings.append(bytes_to_embedding(row[1]))

        hnsw_index.add_batch(content_ids, embeddings)
        logger.info(f"Bootstrapped index with {len(content_ids)} items from PostgreSQL")
    except Exception as e:
        logger.error(f"Error bootstrapping index: {e}")


def kafka_consumer_loop():
    """Background thread: consume content-raw events and add to HNSW index."""
    consumer = Consumer({
        "bootstrap.servers": KAFKA_BOOTSTRAP,
        "group.id": "index-builder",
        "auto.offset.reset": "earliest",
    })
    consumer.subscribe([CONTENT_TOPIC])
    logger.info(f"Kafka consumer started on topic '{CONTENT_TOPIC}'")

    while running:
        msg = consumer.poll(timeout=1.0)
        if msg is None:
            continue
        if msg.error():
            if msg.error().code() == KafkaError._PARTITION_EOF:
                continue
            logger.error(f"Kafka error: {msg.error()}")
            continue

        try:
            event = json.loads(msg.value().decode())
            content_id = event["content_id"]
            embedding = event["embedding"]
            hnsw_index.add(content_id, embedding)
            logger.info(f"Indexed content {content_id} from Kafka")
        except Exception as e:
            logger.error(f"Error processing Kafka message: {e}")

    consumer.close()
    logger.info("Kafka consumer stopped")


@asynccontextmanager
async def lifespan(app: FastAPI):
    global consumer_thread, running

    # Bootstrap from existing DB content
    load_existing_content()

    # Start Kafka consumer in background
    running = True
    consumer_thread = threading.Thread(target=kafka_consumer_loop, daemon=True)
    consumer_thread.start()

    yield

    running = False
    if consumer_thread:
        consumer_thread.join(timeout=5)
    logger.info("Retrieval service stopped")


app = FastAPI(title="Retrieval Service", lifespan=lifespan)


# --- Models ---

class RetrieveRequest(BaseModel):
    query_embedding: list[float]
    top_k: int = 100


class CandidateResult(BaseModel):
    content_id: str
    distance: float


class RetrieveResponse(BaseModel):
    candidates: list[CandidateResult]


# --- Endpoints ---

@app.post("/retrieve", response_model=RetrieveResponse)
def retrieve(req: RetrieveRequest):
    if len(req.query_embedding) != DIM:
        return RetrieveResponse(candidates=[])

    results = hnsw_index.search(req.query_embedding, top_k=req.top_k)
    candidates = [CandidateResult(**r) for r in results]
    return RetrieveResponse(candidates=candidates)


@app.get("/debug/ring")
def debug_ring():
    return hash_ring.info()


@app.get("/debug/index")
def debug_index():
    return hnsw_index.stats()


@app.get("/health")
def health():
    return {"status": "ok", "index_size": hnsw_index.index.get_current_count()}
