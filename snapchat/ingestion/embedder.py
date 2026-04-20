"""Mock embedding generator.

Produces deterministic 128-dim vectors from text. Content in the same
category gets similar embeddings so HNSW searches return meaningful results.
"""

import hashlib
import numpy as np

# Category centroids — each category has a distinct "direction" in embedding space.
# Real Spotlight would use a trained model; we simulate structure via fixed offsets.
CATEGORY_SEEDS = {
    "comedy": 1,
    "sports": 2,
    "music": 3,
    "food": 4,
    "travel": 5,
    "fashion": 6,
    "tech": 7,
    "pets": 8,
    "dance": 9,
    "diy": 10,
}

DIM = 128


def _seeded_vector(seed: int) -> np.ndarray:
    rng = np.random.RandomState(seed)
    vec = rng.randn(DIM).astype(np.float32)
    vec /= np.linalg.norm(vec)
    return vec


# Precompute category centroids
_centroids = {cat: _seeded_vector(s * 1000) for cat, s in CATEGORY_SEEDS.items()}


def embed(text: str, category: str = "") -> list[float]:
    """Generate a 128-dim embedding from text + category.

    The embedding is a mix of:
    - Category centroid (70% weight) — clusters same-category content together
    - Text hash vector (30% weight) — gives each piece of content a unique position
    """
    # Text-specific component
    text_seed = int(hashlib.sha256(text.encode()).hexdigest(), 16) % (2**32)
    text_vec = _seeded_vector(text_seed)

    # Category component (default to "comedy" if unknown)
    cat_vec = _centroids.get(category, _centroids["comedy"])

    # Weighted combination
    vec = 0.7 * cat_vec + 0.3 * text_vec
    vec /= np.linalg.norm(vec)  # re-normalize

    return vec.tolist()
