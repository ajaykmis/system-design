"""Ingestion Service — accepts content uploads and engagement events,
generates embeddings, publishes to Kafka."""

import json
import logging
import os
import struct
import uuid
from contextlib import asynccontextmanager

import psycopg2
import psycopg2.extras
from confluent_kafka import Producer
from fastapi import FastAPI, HTTPException, Header
from pydantic import BaseModel

from embedder import embed

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

# --- Config ---
POSTGRES_DSN = os.getenv("POSTGRES_DSN", "postgresql://snapuser:snappass@localhost:5433/snapchat")
KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "localhost:29092")
CONTENT_TOPIC = "content-raw"
ENGAGEMENT_TOPIC = "engagement-events"

# --- Globals ---
db_conn = None
kafka_producer = None


def get_db():
    global db_conn
    if db_conn is None or db_conn.closed:
        db_conn = psycopg2.connect(POSTGRES_DSN)
        db_conn.autocommit = True
    return db_conn


def get_producer():
    global kafka_producer
    if kafka_producer is None:
        kafka_producer = Producer({"bootstrap.servers": KAFKA_BOOTSTRAP})
    return kafka_producer


def kafka_delivery_report(err, msg):
    if err:
        logger.error(f"Kafka delivery failed: {err}")
    else:
        logger.info(f"Kafka: {msg.topic()}[{msg.partition()}] offset={msg.offset()}")


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("Ingestion service starting")
    get_db()
    get_producer()
    yield
    if kafka_producer:
        kafka_producer.flush()
    if db_conn and not db_conn.closed:
        db_conn.close()
    logger.info("Ingestion service stopped")


app = FastAPI(title="Ingestion Service", lifespan=lifespan)


# --- Models ---

class ContentCreate(BaseModel):
    title: str
    description: str = ""
    category: str = "comedy"
    media_url: str = ""


class ContentResponse(BaseModel):
    content_id: str


class EngagementEvent(BaseModel):
    content_id: str
    event_type: str  # "view", "like", "share"


# --- Helpers ---

def embedding_to_bytes(embedding: list[float]) -> bytes:
    """Pack float32 list to bytes for PostgreSQL BYTEA column."""
    return struct.pack(f"{len(embedding)}f", *embedding)


# --- Endpoints ---

@app.post("/content", response_model=ContentResponse)
def create_content(req: ContentCreate, x_user_id: str = Header(None)):
    if not x_user_id:
        raise HTTPException(status_code=401, detail="missing user context")

    content_id = str(uuid.uuid4())

    # Generate embedding from title + description
    text = f"{req.title} {req.description}".strip()
    embedding = embed(text, req.category)

    # Store in PostgreSQL
    conn = get_db()
    with conn.cursor() as cur:
        cur.execute(
            """INSERT INTO content (id, creator_id, title, description, category, media_url, embedding)
               VALUES (%s, %s, %s, %s, %s, %s, %s)""",
            (content_id, x_user_id, req.title, req.description,
             req.category, req.media_url or f"mock://videos/{content_id}.mp4",
             psycopg2.Binary(embedding_to_bytes(embedding))),
        )

    # Publish to Kafka
    event = {
        "content_id": content_id,
        "creator_id": x_user_id,
        "title": req.title,
        "category": req.category,
        "embedding": embedding,
    }
    producer = get_producer()
    producer.produce(
        CONTENT_TOPIC,
        key=content_id.encode(),
        value=json.dumps(event).encode(),
        callback=kafka_delivery_report,
    )
    producer.poll(0)

    logger.info(f"Content created: {content_id} category={req.category}")
    return ContentResponse(content_id=content_id)


@app.post("/events")
def record_event(req: EngagementEvent, x_user_id: str = Header(None)):
    if not x_user_id:
        raise HTTPException(status_code=401, detail="missing user context")

    event = {
        "user_id": x_user_id,
        "content_id": req.content_id,
        "event_type": req.event_type,
    }
    producer = get_producer()
    producer.produce(
        ENGAGEMENT_TOPIC,
        key=req.content_id.encode(),
        value=json.dumps(event).encode(),
        callback=kafka_delivery_report,
    )
    producer.poll(0)

    logger.info(f"Engagement: {req.event_type} on {req.content_id} by {x_user_id}")
    return {"status": "ok"}


@app.get("/health")
def health():
    return {"status": "ok"}
