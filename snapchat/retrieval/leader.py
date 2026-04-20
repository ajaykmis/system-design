"""Leader election for HNSW index coordination using Redis SETNX + TTL.

Only the leader node rebuilds the HNSW index from Kafka. Followers
load the serialized index from disk. If the leader dies, its lease
expires and another node takes over.

This mirrors the Go election package but in Python for the Retrieval service.
"""

import logging
import threading
import time
import uuid

import redis

logger = logging.getLogger(__name__)


class LeaderElector:
    def __init__(
        self,
        redis_client: redis.Redis,
        election_key: str = "leader:index-builder",
        node_id: str | None = None,
        lease_ttl: int = 30,
    ):
        self.redis = redis_client
        self.key = election_key
        self.node_id = node_id or f"retrieval-{uuid.uuid4().hex[:8]}"
        self.ttl = lease_ttl
        self._is_leader = False
        self._term = 0
        self._lock = threading.Lock()

    def try_acquire(self) -> bool:
        acquired = self.redis.set(self.key, self.node_id, nx=True, ex=self.ttl)
        if acquired:
            with self._lock:
                self._is_leader = True
                self._term += 1
            logger.info(f"Node {self.node_id} became leader (term {self._term})")
        return bool(acquired)

    def renew_lease(self) -> bool:
        current = self.redis.get(self.key)
        if current and current == self.node_id:
            self.redis.expire(self.key, self.ttl)
            return True
        with self._lock:
            self._is_leader = False
        return False

    def resign(self):
        current = self.redis.get(self.key)
        if current and current == self.node_id:
            self.redis.delete(self.key)
        with self._lock:
            self._is_leader = False
        logger.info(f"Node {self.node_id} resigned leadership")

    @property
    def is_leader(self) -> bool:
        with self._lock:
            return self._is_leader

    def get_leader(self) -> str | None:
        val = self.redis.get(self.key)
        return val if val else None

    def status(self) -> dict:
        leader = self.get_leader()
        ttl = self.redis.ttl(self.key)
        return {
            "node_id": self.node_id,
            "is_leader": self.is_leader,
            "current_leader": leader,
            "term": self._term,
            "lease_ttl": self.ttl,
            "lease_remaining": max(ttl, 0),
        }


def run_election_loop(elector: LeaderElector, stop_event: threading.Event):
    """Background thread: continuously try to acquire or renew leadership."""
    interval = elector.ttl / 3
    while not stop_event.is_set():
        try:
            if elector.is_leader:
                if not elector.renew_lease():
                    logger.warning(f"Node {elector.node_id} lost leadership")
            else:
                elector.try_acquire()
        except Exception as e:
            logger.error(f"Election loop error: {e}")
        stop_event.wait(timeout=interval)

    elector.resign()
