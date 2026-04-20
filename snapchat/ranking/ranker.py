"""Scoring model for ranking Spotlight candidates.

Combines ANN distance (relevance) with real-time engagement features
to produce a final score. Uses a simple weighted formula — in production
this would be a trained ML model (LTR, deep ranking, etc.).
"""

import math
import logging

logger = logging.getLogger(__name__)

# Scoring weights — tunable knobs
W_RELEVANCE = 0.4      # ANN distance (closer = more relevant)
W_POPULARITY = 0.25    # view count signal
W_ENGAGEMENT = 0.2     # engagement rate (likes+shares / views)
W_FRESHNESS = 0.15     # recency bonus


def freshness_decay(age_hours: float, half_life: float = 24.0) -> float:
    """Exponential decay: content loses half its freshness bonus every `half_life` hours."""
    return math.exp(-0.693 * age_hours / half_life)


def score_candidate(
    ann_distance: float,
    view_count_1h: int = 0,
    like_count_1h: int = 0,
    share_count_1h: int = 0,
    age_hours: float = 0.0,
) -> float:
    """Score a single candidate. Higher = better.

    Components:
    - relevance: 1 / (1 + distance) — transforms L2 distance to [0, 1]
    - popularity: log(1 + views) — diminishing returns on raw view count
    - engagement: (likes + shares) / max(views, 1) — quality signal
    - freshness: exponential decay with 24h half-life
    """
    relevance = 1.0 / (1.0 + ann_distance)
    popularity = math.log1p(view_count_1h) / 10.0  # normalize ~0-1 range for typical counts
    engagement = (like_count_1h + share_count_1h) / max(view_count_1h, 1)
    freshness = freshness_decay(age_hours)

    score = (
        W_RELEVANCE * relevance
        + W_POPULARITY * popularity
        + W_ENGAGEMENT * engagement
        + W_FRESHNESS * freshness
    )
    return round(score, 6)


def rank_candidates(candidates: list[dict]) -> list[dict]:
    """Score and sort candidates descending by score."""
    for c in candidates:
        c["score"] = score_candidate(
            ann_distance=c.get("distance", 1.0),
            view_count_1h=c.get("view_count_1h", 0),
            like_count_1h=c.get("like_count_1h", 0),
            share_count_1h=c.get("share_count_1h", 0),
            age_hours=c.get("age_hours", 0.0),
        )

    candidates.sort(key=lambda x: x["score"], reverse=True)
    return candidates
