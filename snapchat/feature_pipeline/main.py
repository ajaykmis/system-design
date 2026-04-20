"""Feature Pipeline — stream processor for engagement events.

Consumes engagement events from Kafka, computes windowed aggregations
(tumbling 1-min, sliding 1-hour), and materializes features to Redis.

This is the same pattern as a Flink job:
  Kafka source → keyBy(content_id) → window → aggregate → Redis sink

We implement it with a plain Kafka consumer + manual windowing to make
the internals visible. In production, Flink handles parallelism,
fault tolerance (checkpointing), and exactly-once guarantees.
"""

import json
import logging
import os
import signal
import time

import redis
from confluent_kafka import Consumer, KafkaError

from windows import SlidingWindowCounter, TumblingWindowCounter

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

# --- Config ---
KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "localhost:29092")
REDIS_ADDR = os.getenv("REDIS_ADDR", "localhost:6380")
ENGAGEMENT_TOPIC = "engagement-events"
FLUSH_INTERVAL = 5  # seconds — how often to write aggregates to Redis

# --- Globals ---
running = True


def signal_handler(sig, frame):
    global running
    logger.info("Shutting down...")
    running = False


signal.signal(signal.SIGINT, signal_handler)
signal.signal(signal.SIGTERM, signal_handler)


def main():
    # Redis connection
    host, port = REDIS_ADDR.split(":")
    r = redis.Redis(host=host, port=int(port), decode_responses=True)
    r.ping()
    logger.info(f"Connected to Redis at {REDIS_ADDR}")

    # Window state
    tumbling = TumblingWindowCounter(window_seconds=60)
    sliding = SlidingWindowCounter(window_seconds=3600)

    # Kafka consumer
    consumer = Consumer({
        "bootstrap.servers": KAFKA_BOOTSTRAP,
        "group.id": "feature-pipeline",
        "auto.offset.reset": "earliest",
    })
    consumer.subscribe([ENGAGEMENT_TOPIC])
    logger.info(f"Consuming from '{ENGAGEMENT_TOPIC}'")

    event_count = 0
    last_flush = time.time()

    while running:
        msg = consumer.poll(timeout=1.0)

        if msg is not None and not msg.error():
            try:
                event = json.loads(msg.value().decode())
                content_id = event["content_id"]
                event_type = event["event_type"]

                tumbling.add(content_id, event_type)
                sliding.add(content_id, event_type)
                event_count += 1

            except Exception as e:
                logger.error(f"Error processing event: {e}")

        elif msg is not None and msg.error() and msg.error().code() != KafkaError._PARTITION_EOF:
            logger.error(f"Kafka error: {msg.error()}")

        # Periodic flush to Redis (materialized view)
        now = time.time()
        if now - last_flush >= FLUSH_INTERVAL:
            flush_to_redis(r, sliding)
            last_flush = now
            if event_count > 0:
                logger.info(f"Processed {event_count} events, flushed features to Redis")
                event_count = 0

    consumer.close()
    # Final flush
    flush_to_redis(r, sliding)
    logger.info("Feature pipeline stopped")


def flush_to_redis(r: redis.Redis, sliding: SlidingWindowCounter):
    """Write current sliding window aggregates to Redis.

    This is the 'sink' step — materializing the stream computation
    into a key-value store for the Ranking service to read.
    """
    pipe = r.pipeline()
    flushed = 0

    for content_id in list(sliding.events.keys()):
        counts = sliding.get(content_id)
        if not counts:
            continue

        views = counts.get("view", 0)
        likes = counts.get("like", 0)
        shares = counts.get("share", 0)
        engagement_rate = (likes + shares) / max(views, 1)

        pipe.hset(f"features:{content_id}", mapping={
            "view_count_1h": views,
            "like_count_1h": likes,
            "share_count_1h": shares,
            "engagement_rate": round(engagement_rate, 4),
        })
        # Expire after 2 hours (stale features should disappear)
        pipe.expire(f"features:{content_id}", 7200)
        flushed += 1

    if flushed > 0:
        pipe.execute()


if __name__ == "__main__":
    main()
