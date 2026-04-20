"""Pre-aggregation worker — consumes raw events from Kafka,
increments per-minute counters in Redis sorted sets.

This is what makes the dashboard fast: instead of scanning millions of
raw events at query time, we pre-compute counts as events arrive.

Redis key pattern:
  counts:{event_name}:{minute_bucket}
  e.g., counts:install:2026-04-20T10:05

Each key is a sorted set where members are dimension values ("total",
platform names, etc.) and scores are counts.
"""

import json
import logging
import os
import signal

import redis
from confluent_kafka import Consumer, KafkaError
from datetime import datetime, timezone

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "localhost:29092")
REDIS_ADDR = os.getenv("REDIS_ADDR", "localhost:6380")
TOPIC = "raw-events"
KEY_TTL = 8 * 24 * 3600  # 8 days

running = True


def signal_handler(sig, frame):
    global running
    running = False


signal.signal(signal.SIGINT, signal_handler)
signal.signal(signal.SIGTERM, signal_handler)


def minute_bucket(timestamp_str: str) -> str:
    """Convert an ISO timestamp to a minute bucket string."""
    try:
        dt = datetime.fromisoformat(timestamp_str.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        dt = datetime.now(timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M")


def main():
    host, port = REDIS_ADDR.split(":")
    r = redis.Redis(host=host, port=int(port), decode_responses=True)
    r.ping()
    logger.info(f"Connected to Redis at {REDIS_ADDR}")

    consumer = Consumer({
        "bootstrap.servers": KAFKA_BOOTSTRAP,
        "group.id": "pre-aggregator",
        "auto.offset.reset": "earliest",
    })
    consumer.subscribe([TOPIC])
    logger.info(f"Consuming from '{TOPIC}'")

    batch_count = 0
    pipe = r.pipeline()

    while running:
        msg = consumer.poll(timeout=1.0)

        if msg is None:
            # Flush any pending pipeline commands
            if batch_count > 0:
                pipe.execute()
                logger.info(f"Flushed {batch_count} aggregations to Redis")
                batch_count = 0
                pipe = r.pipeline()
            continue

        if msg.error():
            if msg.error().code() != KafkaError._PARTITION_EOF:
                logger.error(f"Kafka error: {msg.error()}")
            continue

        try:
            event = json.loads(msg.value().decode())
            event_name = event.get("event_name", "unknown")
            ts = event.get("timestamp", "")
            bucket = minute_bucket(ts)
            platform = event.get("properties", {}).get("platform", "unknown")

            key = f"counts:{event_name}:{bucket}"

            # Increment "total" and per-platform counters
            pipe.zincrby(key, 1, "total")
            pipe.zincrby(key, 1, platform)
            pipe.expire(key, KEY_TTL)

            batch_count += 1

            # Flush every 100 events for efficiency
            if batch_count >= 100:
                pipe.execute()
                logger.info(f"Flushed {batch_count} aggregations to Redis")
                batch_count = 0
                pipe = r.pipeline()

        except Exception as e:
            logger.error(f"Error processing event: {e}")

    # Final flush
    if batch_count > 0:
        pipe.execute()

    consumer.close()
    logger.info("Aggregator stopped")


if __name__ == "__main__":
    main()
