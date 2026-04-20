"""Load generator — simulates realistic event traffic for testing the dashboard.

Sends batches of events to the Ingestion API with varying rates
to create interesting patterns on the dashboard.
"""

import json
import random
import time
import sys
from datetime import datetime, timezone

import requests

INGESTION_URL = "http://localhost:8100"

EVENT_TYPES = [
    ("page_load", 50),    # weight: most common
    ("install", 10),
    ("signup", 5),
    ("purchase", 2),
    ("share", 8),
    ("search", 15),
]

PLATFORMS = ["web", "ios", "android"]
PAGES = ["/home", "/feed", "/profile", "/settings", "/search", "/checkout"]


def generate_event() -> dict:
    # Weighted random event type
    events, weights = zip(*EVENT_TYPES)
    event_name = random.choices(events, weights=weights, k=1)[0]

    return {
        "event_name": event_name,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "user_id": f"user_{random.randint(1, 1000)}",
        "device_id": f"dev_{random.randint(1, 500)}",
        "properties": {
            "platform": random.choice(PLATFORMS),
            "page": random.choice(PAGES),
            "ip": f"{random.randint(1,255)}.{random.randint(0,255)}.{random.randint(0,255)}.{random.randint(1,255)}",
        },
    }


def run(events_per_second: int = 20, duration_seconds: int = 300):
    batch_size = max(1, events_per_second // 4)  # 4 batches per second
    interval = 1.0 / 4

    total_sent = 0
    start = time.time()
    end = start + duration_seconds

    print(f"Generating ~{events_per_second} events/sec for {duration_seconds}s (batch={batch_size})")
    print(f"Dashboard: http://localhost:8101/static/")
    print()

    while time.time() < end:
        batch = [generate_event() for _ in range(batch_size)]

        # Add occasional spike (simulate viral moment)
        if random.random() < 0.05:
            spike = [generate_event() for _ in range(batch_size * 3)]
            batch.extend(spike)

        try:
            resp = requests.post(
                f"{INGESTION_URL}/v1/events",
                json={"events": batch},
                timeout=5,
            )
            total_sent += len(batch)

            elapsed = time.time() - start
            rate = total_sent / elapsed if elapsed > 0 else 0
            sys.stdout.write(f"\r  Sent: {total_sent:,} events ({rate:.0f}/sec)")
            sys.stdout.flush()
        except Exception as e:
            print(f"\n  Error: {e}")

        time.sleep(interval)

    elapsed = time.time() - start
    print(f"\n\nDone: {total_sent:,} events in {elapsed:.0f}s ({total_sent/elapsed:.0f}/sec)")


if __name__ == "__main__":
    eps = int(sys.argv[1]) if len(sys.argv) > 1 else 20
    dur = int(sys.argv[2]) if len(sys.argv) > 2 else 300
    run(events_per_second=eps, duration_seconds=dur)
