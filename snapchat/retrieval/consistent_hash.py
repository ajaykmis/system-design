"""Consistent hash ring for distributing content across index shards.

In the MVP we have a single shard, but the ring is fully functional
and exposed via a debug endpoint to demonstrate the concept.
"""

import hashlib
import bisect


class ConsistentHashRing:
    def __init__(self, nodes: list[str], vnodes_per_node: int = 150):
        self.vnodes_per_node = vnodes_per_node
        self.ring: list[tuple[int, str]] = []  # (hash, node)
        self.hashes: list[int] = []  # sorted for bisect
        self.nodes = set(nodes)

        for node in nodes:
            self._add_node(node)

    def _hash(self, key: str) -> int:
        return int(hashlib.md5(key.encode()).hexdigest(), 16)

    def _add_node(self, node: str):
        for i in range(self.vnodes_per_node):
            h = self._hash(f"{node}:{i}")
            idx = bisect.bisect_left(self.hashes, h)
            self.hashes.insert(idx, h)
            self.ring.insert(idx, (h, node))

    def get_node(self, key: str) -> str:
        """Return the node responsible for the given key."""
        if not self.ring:
            raise ValueError("empty ring")
        h = self._hash(key)
        idx = bisect.bisect_right(self.hashes, h) % len(self.hashes)
        return self.ring[idx][1]

    def get_distribution(self, keys: list[str]) -> dict[str, int]:
        """Show how keys distribute across nodes (for debug/testing)."""
        dist: dict[str, int] = {n: 0 for n in self.nodes}
        for key in keys:
            node = self.get_node(key)
            dist[node] += 1
        return dist

    def info(self) -> dict:
        return {
            "nodes": sorted(self.nodes),
            "vnodes_per_node": self.vnodes_per_node,
            "total_vnodes": len(self.ring),
        }
