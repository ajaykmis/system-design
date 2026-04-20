"""Windowed aggregation for engagement events.

Implements two window types that mirror Flink's windowing semantics:

1. TumblingWindow (1 min) — non-overlapping, fixed-size windows.
   Counts events in the current minute. When the minute rolls over,
   the previous window is emitted and the counter resets.

2. SlidingWindow (1 hour) — tracks events in a sliding time range.
   Uses a deque of (timestamp, count) tuples. Old entries are evicted
   as they fall outside the window. This is how Flink's sliding windows
   work internally — a ring buffer of micro-batches.

In production Flink, these run as parallel operators across partitioned
streams. Here we run single-threaded per-partition (keyed by content_id)
to demonstrate the same semantics.
"""

import time
from collections import defaultdict, deque


class TumblingWindowCounter:
    """Counts events per key in fixed 60-second windows."""

    def __init__(self, window_seconds: int = 60):
        self.window_seconds = window_seconds
        self.counts: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
        self.window_start: dict[str, float] = {}

    def add(self, key: str, event_type: str, timestamp: float | None = None):
        ts = timestamp or time.time()
        window_id = int(ts // self.window_seconds)

        if key not in self.window_start:
            self.window_start[key] = window_id

        # Window rolled over — reset counts
        if window_id != self.window_start.get(key):
            self.counts[key] = defaultdict(int)
            self.window_start[key] = window_id

        self.counts[key][event_type] += 1

    def get(self, key: str) -> dict[str, int]:
        return dict(self.counts.get(key, {}))


class SlidingWindowCounter:
    """Tracks events per key over a sliding time window.

    Internally uses a deque of (timestamp, event_type) per key.
    On each query, evicts entries older than the window.
    """

    def __init__(self, window_seconds: int = 3600):
        self.window_seconds = window_seconds
        self.events: dict[str, deque] = defaultdict(deque)

    def add(self, key: str, event_type: str, timestamp: float | None = None):
        ts = timestamp or time.time()
        self.events[key].append((ts, event_type))

    def _evict(self, key: str, now: float):
        """Remove events older than the window."""
        cutoff = now - self.window_seconds
        q = self.events[key]
        while q and q[0][0] < cutoff:
            q.popleft()

    def get(self, key: str) -> dict[str, int]:
        now = time.time()
        self._evict(key, now)

        counts: dict[str, int] = defaultdict(int)
        for _, event_type in self.events[key]:
            counts[event_type] += 1
        return dict(counts)

    def get_rate(self, key: str) -> float:
        """Engagement rate: (likes + shares) / max(views, 1)."""
        counts = self.get(key)
        views = counts.get("view", 0)
        likes = counts.get("like", 0)
        shares = counts.get("share", 0)
        return (likes + shares) / max(views, 1)
