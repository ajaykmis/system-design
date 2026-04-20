"""Seed script: generate fake content with embeddings and push via the Ingestion API."""

import random
import requests
import sys

INGESTION_URL = "http://localhost:8090"

CATEGORIES = ["comedy", "sports", "music", "food", "travel", "fashion", "tech", "pets", "dance", "diy"]

TITLES = {
    "comedy": ["Funny fails compilation", "Try not to laugh challenge", "Prank gone wrong", "Stand-up clip", "Meme review"],
    "sports": ["Basketball highlights", "Soccer goal of the week", "Gym workout routine", "Skateboard tricks", "Boxing knockout"],
    "music": ["Guitar cover", "Beat drop compilation", "Singing in public", "Piano tutorial", "DJ mix session"],
    "food": ["5-min recipe hack", "Street food tour", "Cooking challenge", "Taste test review", "Baking tutorial"],
    "travel": ["Hidden gem destination", "Backpacking vlog", "Hotel room tour", "Sunset timelapse", "Road trip highlights"],
    "fashion": ["Outfit of the day", "Thrift haul", "Style transformation", "Sneaker unboxing", "Fashion week recap"],
    "tech": ["New phone review", "Coding tutorial", "Setup tour", "AI demo", "Gadget unboxing"],
    "pets": ["Puppy compilation", "Cat vs cucumber", "Parrot talking", "Bunny zoomies", "Dog training tips"],
    "dance": ["Viral dance challenge", "Choreography tutorial", "Dance battle", "Flash mob", "Ballet practice"],
    "diy": ["Room makeover", "Life hack compilation", "Craft tutorial", "Furniture build", "Organization tips"],
}


def seed(user_id: str, count: int = 100):
    created = 0
    for i in range(count):
        category = random.choice(CATEGORIES)
        base_title = random.choice(TITLES[category])
        title = f"{base_title} #{random.randint(1, 9999)}"
        description = f"Amazing {category} content you need to see"

        resp = requests.post(
            f"{INGESTION_URL}/content",
            json={"title": title, "description": description, "category": category},
            headers={"X-User-ID": user_id},
        )
        if resp.status_code == 200:
            created += 1
            if created % 25 == 0:
                print(f"  Created {created}/{count}")
        else:
            print(f"  ERROR: {resp.status_code} {resp.text}")

    print(f"Done: {created}/{count} content items created")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python seed_content.py <user_id> [count]")
        print("  Get user_id from: docker exec snap-postgres psql -U snapuser -d snapchat -t -c 'SELECT id FROM users LIMIT 1;'")
        sys.exit(1)

    user_id = sys.argv[1].strip()
    count = int(sys.argv[2]) if len(sys.argv) > 2 else 100
    print(f"Seeding {count} content items for user {user_id}...")
    seed(user_id, count)
