# Delayed Payment Scheduler Service Design

Summary
Delayed Payment Scheduler Design Approach: Start with the naive solution - simple API + database + cron poller - then immediately identify the core problems: single point of failure, poor scalability, no fund reservation, and operational blindness. Evolve to event-driven architecture using message queues with delay capabilities, separating concerns into API service, balance service (with immediate fund reservation), and distributed payment processors. Key design decisions: choose message queues over polling for efficiency, reserve funds immediately for financial accuracy, use strong consistency for payments but eventual consistency for queries, and implement horizontal sharding by user ID. Address production concerns with comprehensive monitoring, circuit breakers, retry mechanisms with exponential backoff, and proper audit trails. The final architecture handles millions of concurrent holds through microservices that can scale independently, eliminates race conditions through proper locking and idempotency, and maintains financial accuracy while providing operational excellence at Roblox scale.


## 1. Naive System Design

### Core Architecture
The initial approach uses a simple three-component system: an API service for creating and managing payment holds, a database for storing hold information, and a background poller that checks for due payments every minute.

The API service handles incoming requests to create delayed payments, storing them in a single table with fields for sender, recipient, amount, creation time, execution time, and status. A background job runs continuously, querying the database every minute for payments where the execution time has passed, then processing them sequentially.

### Data Storage
Payment holds are stored in a single table with basic fields: unique identifier, sender and recipient user IDs, Robux amount, timestamps for creation and execution, and a simple status enum. No partitioning, indexing strategy, or audit trail is implemented initially.

### Processing Logic
When creating a hold, the system validates users exist, checks sender balance, and stores the hold record. The background poller queries for due payments, processes each one by transferring Robux between user accounts, and updates the hold status to completed. Error handling is minimal, with failed payments simply logged.

## 2. Critical Challenges with Naive Approach

### Scalability Bottlenecks
The single poller becomes a bottleneck as transaction volume grows, unable to process payments fast enough during peak periods. Database queries perform poorly as the table grows, especially time-based scans across millions of records. Memory usage becomes problematic when loading large batches of due payments.

### Reliability Failures
The system has a single point of failure - if the poller process dies, no payments execute. Multiple poller instances create race conditions where the same payment might be processed twice. There's no retry mechanism for temporary failures, and partial system failures can leave payments in inconsistent states.

### Operational Blindness
No metrics exist for processing delays, failure rates, or system health. Debugging issues requires manual database queries with no audit trail. The fixed polling interval wastes resources during low-activity periods and creates delays during high-activity periods.

### Financial Accuracy Issues
Funds aren't reserved when holds are created, leading to potential insufficient balance scenarios at execution time. No transaction guarantees exist across user balance updates. Race conditions can occur when users spend reserved funds before hold execution.

## 3. Improved Architecture

### Event-Driven Design
The improved system replaces polling with an event-driven architecture using message queues. When a hold is created, a delayed message is scheduled in a queue system that automatically delivers the message at the specified execution time. This eliminates the need for continuous database polling and provides natural load distribution.

### Service Decomposition
The monolithic approach is split into specialized services: an API service for hold management, a dedicated balance service for fund reservations and transfers, and payment processors that consume messages from queues. Each service can scale independently based on its specific load patterns.

### Enhanced Data Model
The payment holds table is partitioned by execution date for query performance and easy archival. Additional fields track retry attempts, error details, reserved balance references, and version numbers for optimistic locking. A separate audit events table maintains an immutable history of all state changes for debugging and compliance.

### Queue-Based Processing
Two queue types handle different phases: a delay queue that holds messages until execution time, and a processing queue for immediate payment execution. Failed payments are retried with exponential backoff, and persistent failures move to a dead letter queue for manual intervention.

### Fund Reservation Strategy
Funds are reserved immediately when a hold is created, preventing overspend scenarios. The balance service maintains separate reserved and available balance tracking. If a hold is cancelled or fails permanently, the reservation is released back to the user's available balance.

## 4. Advanced Optimizations

### Horizontal Scaling Strategy
```
                    ┌─────────────────┐
                    │  Load Balancer  │
                    └─────────┬───────┘
                              │
            ┌─────────────────┼─────────────────┐
            │                 │                 │
            ▼                 ▼                 ▼
    ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
    │ API Service  │  │ API Service  │  │ API Service  │
    │   Shard A    │  │   Shard B    │  │   Shard C    │
    └──────┬───────┘  └──────┬───────┘  └──────┬───────┘
           │                 │                 │
    ┌──────▼───────┐  ┌──────▼───────┐  ┌──────▼───────┐
    │   DB Shard   │  │   DB Shard   │  │   DB Shard   │
    │   Users      │  │   Users      │  │   Users      │
    │   1-33M      │  │   34-67M     │  │   68-100M    │
    └──────────────┘  └──────────────┘  └──────────────┘
```

Payment holds are sharded across multiple database instances based on user ID, distributing both storage and query load. Each shard can be scaled independently based on user activity patterns. Processing nodes can be added or removed dynamically based on queue depth and processing lag metrics.

### Time-Based Partitioning
Database tables are partitioned by execution date, enabling efficient queries for due payments and automated cleanup of old data. Partitions are created automatically for future dates and dropped after retention periods. This approach maintains query performance as data volume grows.

### Multi-Layer Caching
Frequently accessed data like user pending holds and balance information is cached using Redis with appropriate TTL values. Cache invalidation strategies ensure consistency when holds are created, modified, or processed. User-specific caches enable fast response times for status queries.

### Intelligent Batching
Related payments are batched together for processing efficiency, particularly for high-volume users or during peak periods. Batch sizes are dynamically adjusted based on processing capacity and queue depth. Failed batch items are individually retried to prevent good payments from being blocked by bad ones.

## 5. Production Considerations

### Monitoring and Alerting
Comprehensive metrics track hold creation rates, processing delays, error rates, and queue depths. Alerting systems notify operators of processing delays, high error rates, or system component failures. Business metrics monitor payment volume trends and user behavior patterns.

### Capacity Management
System capacity is planned based on peak transaction volumes with appropriate headroom. Queue depths and processing lag are monitored to trigger auto-scaling events. Database connection pools and message queue consumers are sized to handle expected load patterns.

### Security and Compliance
All payment operations require proper authentication and authorization. Input validation prevents negative amounts, self-payments, and amounts exceeding configured limits. Audit trails maintain immutable records of all financial transactions for compliance requirements.

### Error Recovery Procedures
Circuit breakers prevent cascade failures when dependent services are unavailable. Graceful degradation allows read-only operations during partial outages. Automated recovery procedures handle common failure scenarios, while escalation procedures ensure human intervention for complex issues.

### Configuration Management
Feature flags enable gradual rollout of new functionality and quick rollback if issues arise. Configuration parameters like maximum delay times, retry policies, and processing limits can be adjusted without code deployment. A/B testing capabilities allow experimentation with different processing strategies.

## 6. Trade-offs and Design Decisions

### Message Queue vs Database Polling
**Decision**: Message queue with delay capability
**Reasoning**: Event-driven processing is more resource-efficient and provides better scalability than continuous polling. The additional infrastructure complexity is justified by improved performance and reduced database load. A hybrid fallback using database polling provides additional reliability during queue outages.

### Immediate vs Delayed Fund Reservation
**Decision**: Immediate reservation at hold creation
**Reasoning**: Guaranteeing fund availability is crucial for user trust and financial accuracy. While this locks up user funds temporarily, it prevents the poor user experience of failed payments due to insufficient funds at execution time. The complexity of reservation management is acceptable given the benefits.

### Strong vs Eventual Consistency
**Decision**: Strong consistency for financial operations, eventual consistency for user queries
**Reasoning**: Financial accuracy requires strong consistency for balance updates and payment processing. User queries about hold status can use eventually consistent read replicas for better performance, as slight delays in status updates are acceptable.

### Microservices vs Monolithic Architecture
**Decision**: Microservices with clear service boundaries
**Reasoning**: At Roblox's scale, independent scaling of payment components is essential. The network complexity and distributed transaction challenges are manageable with proper design, while the benefits of fault isolation and technology diversity outweigh the costs.

### Synchronous vs Asynchronous Processing
**Decision**: Asynchronous processing with synchronous status APIs
**Reasoning**: Asynchronous processing provides better scalability and fault tolerance. Users can query hold status synchronously when needed, providing the best of both approaches. This design handles load spikes gracefully while maintaining good user experience.

### In-Memory vs Persistent State
**Decision**: Persistent state with in-memory caching
**Reasoning**: Financial data must be persisted for durability and audit requirements. In-memory caching provides performance benefits for frequently accessed data while maintaining data integrity. Cache invalidation strategies ensure consistency between cache and persistent storage.

This architecture provides a robust foundation for processing millions of delayed payments while maintaining financial accuracy, system reliability, and operational excellence required for a platform of Roblox's scale.
