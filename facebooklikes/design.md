# Like System Architecture Evolution
*System Design Interview Walkthrough*

## Requirements Clarification
- **Scale**: 1B users, 100M posts, 10B likes/day
- **Reads**: 100:1 read/write ratio
- **Latency**: <100ms for reads, <500ms for writes
- **Consistency**: Eventually consistent (AP over C)
- **Features**: Like/unlike, view counts, who liked

---

## Stage 1: Single Database (MVP - 10K users)

### Architecture
```
[Mobile App] → [Load Balancer] → [Web Server] → [MySQL Database]
```

### Database Schema
```sql
-- Simple approach
CREATE TABLE likes (
    user_id BIGINT,
    post_id BIGINT,
    created_at TIMESTAMP,
    PRIMARY KEY (user_id, post_id),
    INDEX idx_post_id (post_id)
);

-- Get like count
SELECT COUNT(*) FROM likes WHERE post_id = ?;

-- Check if user liked
SELECT 1 FROM likes WHERE user_id = ? AND post_id = ?;
```

### Bottlenecks
- Single point of failure
- COUNT(*) queries become slow
- Write contention on popular posts
- No geographic distribution

---

## Stage 2: Read Replicas + Caching (100K users)

### Architecture
```
[Mobile Apps] → [Load Balancer] → [Web Servers]
                                        ↓
                                   [Redis Cache]
                                        ↓
                    [Master DB] ← replicate → [Read Replicas]
```

### Optimizations
```python
# Cache layer implementation
class LikeService:
    def get_like_count(self, post_id):
        # Try cache first
        count = redis.get(f"like_count:{post_id}")
        if count is None:
            # Fall back to read replica
            count = db_replica.query("SELECT COUNT(*) FROM likes WHERE post_id = ?", post_id)
            redis.setex(f"like_count:{post_id}", 3600, count)
        return count
    
    def add_like(self, user_id, post_id):
        # Write to master
        db_master.execute("INSERT INTO likes VALUES (?, ?, NOW())", user_id, post_id)
        # Invalidate cache
        redis.delete(f"like_count:{post_id}")
```

### Issues Remaining
- Cache invalidation complexity
- Still counting individual rows
- Master becomes write bottleneck
- Replication lag affects consistency ==> Master to Read Replica

---

## Stage 3: Denormalized Counters (1M users)

### Schema Evolution
```sql
-- Add counter table for performance
CREATE TABLE post_stats (
    post_id BIGINT PRIMARY KEY,
    like_count INT DEFAULT 0,
    updated_at TIMESTAMP
);

-- Likes table for who liked (sparse)
CREATE TABLE likes (
    user_id BIGINT,
    post_id BIGINT,
    created_at TIMESTAMP,
    PRIMARY KEY (user_id, post_id)
);
```

### Write Strategy
```python
def add_like(self, user_id, post_id):
    with db_transaction():
        # Insert like record
        db.execute("INSERT INTO likes VALUES (?, ?, NOW())", user_id, post_id)
        # Increment counter
        db.execute("UPDATE post_stats SET like_count = like_count + 1 WHERE post_id = ?", post_id)
        # Update cache
        redis.incr(f"like_count:{post_id}")
```

### Problems at Scale
- Hot partition problem (viral posts)
- Transaction overhead
- Lock contention on popular posts

---

## Stage 4: Sharding + Async Processing (10M users)

### Sharded Architecture
```
[Load Balancer] → [App Servers]
                       ↓
                [Message Queue] → [Counter Workers]
                       ↓              ↓
              [Shard 1] [Shard 2] [Shard 3] [Shard N]
```

### Sharding Strategy
```python
class ShardedLikeService:
    def get_shard(self, post_id):
        return post_id % self.num_shards
    
    def add_like_async(self, user_id, post_id):
        # Immediate response
        event = {
            'type': 'LIKE_ADDED',
            'user_id': user_id,
            'post_id': post_id,
            'timestamp': time.now()
        }
        message_queue.publish('likes', event)
        
        # Update cache optimistically
        redis.incr(f"like_count:{post_id}")
        return "OK"
```

### Background Processing
```python
class LikeProcessor:
    def process_like_event(self, event):
        shard = self.get_shard(event['post_id'])
        
        # Write to appropriate shard
        shard_db = self.get_shard_db(shard)
        shard_db.execute(
            "INSERT INTO likes VALUES (?, ?, ?)",
            event['user_id'], event['post_id'], event['timestamp']
        )
        
        # Update counter
        shard_db.execute(
            "UPDATE post_stats SET like_count = like_count + 1 WHERE post_id = ?",
            event['post_id']
        )
```

---

## Stage 5: Event Sourcing + CRDT Counters (100M users)

### Event-Driven Architecture
```
[Mobile Apps] → [API Gateway] → [Like Service]
                                      ↓
                               [Event Stream (Kafka)]
                                      ↓
    [Counter Service] ← [Analytics] ← [Stream Processors] → [Notification Service]
           ↓
    [Distributed Cache] + [Counter Shards]
```

### CRDT Implementation
```python
class GCounterShard:
    def __init__(self, shard_id):
        self.shard_id = shard_id
        self.counters = {}  # replica_id -> count
    
    def increment(self, replica_id):
        self.counters[replica_id] = self.counters.get(replica_id, 0) + 1
    
    def value(self):
        return sum(self.counters.values())
    
    def merge(self, other_shard):
        for replica_id, count in other_shard.counters.items():
            self.counters[replica_id] = max(
                self.counters.get(replica_id, 0), 
                count
            )
```

### Event Processing
```python
class LikeEventProcessor:
    def handle_like_event(self, event):
        post_id = event['post_id']
        region = event['region']
        
        # Increment regional counter
        counter_shard = self.get_counter_shard(post_id, region)
        counter_shard.increment(region)
        
        # Publish for cross-region sync
        self.publish_counter_update(post_id, region, counter_shard.value())
        
        # Update cache
        total_count = self.aggregate_all_shards(post_id)
        cache.set(f"like_count:{post_id}", total_count, ttl=60)
```

---

## Stage 6: Global Scale with Approximation (1B users)

### Multi-Region Architecture
```
Region US-East:  [Apps] → [Like Service] → [Kafka] → [Counter Service]
                                              ↓
Region EU:       [Apps] → [Like Service] → [Kafka] → [Counter Service]
                                              ↓
Region Asia:     [Apps] → [Like Service] → [Kafka] → [Counter Service]
                                              ↓
                        [Global Aggregation Service]
                                              ↓
                           [Global Cache Layer]
```

### Approximate Counting (Your Point #1)
```python
class ApproximateCounter:
    def __init__(self, precision_threshold=1000):
        self.threshold = precision_threshold
        self.exact_count = 0
        self.approximate_multiplier = 1
    
    def increment(self):
        if self.exact_count < self.threshold:
            self.exact_count += 1
        else:
            # Switch to approximate counting
            if random.random() < (1.0 / self.approximate_multiplier):
                self.exact_count += self.approximate_multiplier
            # Increase approximation for very large numbers
            if self.exact_count > self.threshold * 10:
                self.approximate_multiplier *= 2
    
    def get_display_count(self):
        if self.exact_count < 1000:
            return str(self.exact_count)
        elif self.exact_count < 1000000:
            return f"{self.exact_count//1000}K"
        else:
            return f"{self.exact_count//1000000}M"
```

### HyperLogLog for Unique Likers
```python
class UniqueLikerCounter:
    def __init__(self):
        self.hll = HyperLogLog(precision=12)  # ~1.6% error rate
    
    def add_liker(self, user_id):
        self.hll.add(str(user_id))
    
    def unique_liker_count(self):
        return int(self.hll.cardinality())
```

### Regional Aggregation (Your Point #2)
```python
class GlobalLikeAggregator:
    def aggregate_post_likes(self, post_id):
        regional_counts = {}
        
        # Collect from each region
        for region in ['us-east', 'eu-west', 'asia-pacific']:
            regional_counts[region] = self.get_regional_count(post_id, region)
        
        # Merge CRDT counters
        global_counter = GCounterShard('global')
        for region, counter in regional_counts.items():
            global_counter.merge(counter)
        
        return global_counter.value()
    
    def periodic_reconciliation(self):
        # Run every 5 minutes
        for post_id in self.get_active_posts():
            global_count = self.aggregate_post_likes(post_id)
            cache.set(f"global_like_count:{post_id}", global_count, ttl=300)
```

---

## Interview Discussion Points

### Key Design Decisions
1. **Consistency Model**: Choose AP over C (eventual consistency)
2. **Partitioning Strategy**: By post_id for even distribution
3. **Caching Strategy**: Multi-layer (regional + global)
4. **Approximation Trade-offs**: Accuracy vs performance

### Monitoring & Operations
```python
# Key metrics to track
metrics = {
    'like_events_per_second': 50000,
    'cache_hit_rate': 0.95,
    'regional_sync_lag': '< 100ms',
    'approximation_error': '< 2%',
    'p99_read_latency': '< 50ms'
}
```

### Failure Scenarios
- **Regional outage**: Serve from other regions
- **Cache failure**: Fall back to database
- **Message queue lag**: Back-pressure + circuit breakers
- **Count inconsistencies**: Reconciliation jobs

### Cost Optimization
- **Storage tiering**: Hot data in memory, cold in disk
- **Compression**: Batch similar events
- **Sampling**: Only process subset of events for analytics

---

## System Design Interview Tips

1. **Start simple**: Always begin with single database
2. **Identify bottlenecks**: Point out issues at each stage
3. **Justify trade-offs**: Explain why eventual consistency is okay
4. **Show evolution**: Demonstrate scaling thinking
5. **Consider edge cases**: Viral posts, celebrity accounts
6. **Discuss monitoring**: How you'd detect and fix issues

This architecture evolution shows the journey from a simple like system to a global-scale distributed system, with each stage addressing specific bottlenecks while introducing new complexities.
