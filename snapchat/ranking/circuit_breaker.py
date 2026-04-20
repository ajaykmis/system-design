"""Circuit breaker for protecting the Ranking service from slow/failed dependencies.

States:
  CLOSED  → normal operation, requests pass through
  OPEN    → dependency is failing, requests are rejected immediately (fail fast)
  HALF_OPEN → after a cooldown, allow one test request through

Transitions:
  CLOSED → OPEN: when failure_count >= threshold
  OPEN → HALF_OPEN: after cooldown_seconds
  HALF_OPEN → CLOSED: if test request succeeds
  HALF_OPEN → OPEN: if test request fails
"""

import logging
import threading
import time
from enum import Enum
from typing import Callable, TypeVar

logger = logging.getLogger(__name__)

T = TypeVar("T")


class State(Enum):
    CLOSED = "closed"
    OPEN = "open"
    HALF_OPEN = "half_open"


class CircuitBreaker:
    def __init__(
        self,
        name: str,
        failure_threshold: int = 5,
        cooldown_seconds: float = 30.0,
    ):
        self.name = name
        self.failure_threshold = failure_threshold
        self.cooldown_seconds = cooldown_seconds

        self._lock = threading.Lock()
        self._state = State.CLOSED
        self._failure_count = 0
        self._last_failure_time = 0.0
        self._total_trips = 0

    @property
    def state(self) -> State:
        with self._lock:
            if self._state == State.OPEN:
                if time.time() - self._last_failure_time >= self.cooldown_seconds:
                    self._state = State.HALF_OPEN
                    logger.info(f"[circuit:{self.name}] OPEN → HALF_OPEN (cooldown expired)")
            return self._state

    def call(self, fn: Callable[[], T], fallback: Callable[[], T] | None = None) -> T:
        """Execute fn through the circuit breaker. Uses fallback if circuit is open."""
        state = self.state

        if state == State.OPEN:
            logger.warning(f"[circuit:{self.name}] OPEN — using fallback")
            if fallback:
                return fallback()
            raise CircuitOpenError(f"Circuit '{self.name}' is open")

        try:
            result = fn()
            self._on_success()
            return result
        except Exception as e:
            self._on_failure()
            if fallback and self.state == State.OPEN:
                logger.warning(f"[circuit:{self.name}] Call failed, using fallback: {e}")
                return fallback()
            raise

    def _on_success(self):
        with self._lock:
            if self._state == State.HALF_OPEN:
                self._state = State.CLOSED
                self._failure_count = 0
                logger.info(f"[circuit:{self.name}] HALF_OPEN → CLOSED (success)")
            elif self._state == State.CLOSED:
                self._failure_count = 0

    def _on_failure(self):
        with self._lock:
            self._failure_count += 1
            self._last_failure_time = time.time()

            if self._state == State.HALF_OPEN:
                self._state = State.OPEN
                self._total_trips += 1
                logger.warning(f"[circuit:{self.name}] HALF_OPEN → OPEN (test failed)")
            elif self._failure_count >= self.failure_threshold:
                self._state = State.OPEN
                self._total_trips += 1
                logger.warning(
                    f"[circuit:{self.name}] CLOSED → OPEN "
                    f"({self._failure_count} failures, trip #{self._total_trips})"
                )

    def status(self) -> dict:
        return {
            "name": self.name,
            "state": self.state.value,
            "failure_count": self._failure_count,
            "failure_threshold": self.failure_threshold,
            "cooldown_seconds": self.cooldown_seconds,
            "total_trips": self._total_trips,
        }


class CircuitOpenError(Exception):
    pass
