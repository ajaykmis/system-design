"""HNSW index for approximate nearest neighbor search.

Uses hnswlib to build and query a hierarchical navigable small-world graph.
The leader node builds/updates the index; followers load from disk.
"""

import logging
import os
import threading

import hnswlib
import numpy as np

logger = logging.getLogger(__name__)

DIM = 128
INDEX_DIR = os.getenv("INDEX_DIR", "/tmp/snap-hnsw-index")


class HNSWIndex:
    def __init__(self, dim: int = DIM, max_elements: int = 100_000):
        self.dim = dim
        self.max_elements = max_elements
        self.lock = threading.Lock()

        # id_map: internal integer id <-> content_id string
        self.id_to_content: dict[int, str] = {}
        self.content_to_id: dict[str, int] = {}
        self.next_id = 0

        # Initialize empty index
        self.index = hnswlib.Index(space="l2", dim=dim)
        self.index.init_index(
            max_elements=max_elements,
            M=16,               # connections per layer (higher = better recall, more memory)
            ef_construction=200, # build-time search width (higher = better index quality)
        )
        self.index.set_ef(50)  # query-time search width (higher = better recall, slower)

        os.makedirs(INDEX_DIR, exist_ok=True)

    def add(self, content_id: str, embedding: list[float]):
        """Add a single item to the index."""
        with self.lock:
            if content_id in self.content_to_id:
                return  # already indexed

            vec = np.array(embedding, dtype=np.float32).reshape(1, -1)
            internal_id = self.next_id
            self.next_id += 1

            # Resize if needed
            if internal_id >= self.index.get_max_elements():
                self.index.resize_index(self.index.get_max_elements() * 2)

            self.index.add_items(vec, np.array([internal_id]))
            self.id_to_content[internal_id] = content_id
            self.content_to_id[content_id] = internal_id

    def add_batch(self, content_ids: list[str], embeddings: list[list[float]]):
        """Add multiple items at once (more efficient)."""
        with self.lock:
            new_ids = []
            new_vecs = []
            for cid, emb in zip(content_ids, embeddings):
                if cid in self.content_to_id:
                    continue
                internal_id = self.next_id
                self.next_id += 1
                self.id_to_content[internal_id] = cid
                self.content_to_id[cid] = internal_id
                new_ids.append(internal_id)
                new_vecs.append(emb)

            if not new_vecs:
                return

            # Resize if needed
            needed = self.next_id
            if needed > self.index.get_max_elements():
                self.index.resize_index(max(needed * 2, self.index.get_max_elements() * 2))

            vecs = np.array(new_vecs, dtype=np.float32)
            ids = np.array(new_ids, dtype=np.int64)
            self.index.add_items(vecs, ids)
            logger.info(f"Added {len(new_ids)} items to index (total: {self.index.get_current_count()})")

    def search(self, query_embedding: list[float], top_k: int = 100) -> list[dict]:
        """Search for nearest neighbors. Returns list of {content_id, distance}."""
        with self.lock:
            if self.index.get_current_count() == 0:
                return []

            query = np.array(query_embedding, dtype=np.float32).reshape(1, -1)
            k = min(top_k, self.index.get_current_count())
            labels, distances = self.index.knn_query(query, k=k)

            results = []
            for label, dist in zip(labels[0], distances[0]):
                cid = self.id_to_content.get(int(label))
                if cid:
                    results.append({"content_id": cid, "distance": float(dist)})
            return results

    def save(self, path: str | None = None):
        """Save index to disk."""
        path = path or os.path.join(INDEX_DIR, "index.bin")
        with self.lock:
            self.index.save_index(path)
            logger.info(f"Index saved to {path} ({self.index.get_current_count()} items)")

    def load(self, path: str | None = None):
        """Load index from disk."""
        path = path or os.path.join(INDEX_DIR, "index.bin")
        if os.path.exists(path):
            with self.lock:
                self.index.load_index(path, max_elements=self.max_elements)
                logger.info(f"Index loaded from {path}")

    def stats(self) -> dict:
        return {
            "total_items": self.index.get_current_count(),
            "max_elements": self.index.get_max_elements(),
            "ef_search": self.index.ef,
            "M": 16,
            "ef_construction": 200,
            "space": "l2",
            "dim": self.dim,
        }
