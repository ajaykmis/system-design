# Design a Throttling System

**Staff Engineer System Design Interview**  
*Estimated Duration: 45-60 minutes*

---

## Understanding the Problem

### 🚦 What is a Throttling System?
A throttling system (also known as rate limiting) is a mechanism that controls the rate of requests or operations to protect services from being overwhelmed. It acts as a safety valve in distributed systems, preventing cascading failures when traffic spikes occur. Unlike simple load balancing, throttling makes intelligent decisions about which requests to allow, delay, or reject based on various factors like user identity, request type, and system health.

**Current Architecture Context**:
```
Client → HTTP Server (Gateway) → API Server → Database/3P/Other APIs
```

The system is experiencing cascading failures due to traffic bursts, requiring a comprehensive throttling solution across all layers.

---

## Functional Requirements

### Core Requirements
1. **Multi-layer Rate Limiting**: Throttle at gateway, API server, and resource levels
2. **Multiple Throttling Dimensions**: Support user-based, IP-based, endpoint-based, and global throttling
3. **Dynamic Threshold Management**: Adjust limits based on system health and capacity
4. **Request Classification**: Different limits for different request types/priorities
5. **Graceful Degradation**: Fail safely when throttling components are unavailable

### Below the line (out of scope)
- Complex ML-based traffic prediction
- Detailed billing/monetization integration
- Advanced DDoS protection mechanisms
- Fine-grained permission-based throttling
- Multi-region coordination (single region focus)

---

## Non-Functional Requirements

**Scale Assumptions** (confirm with interviewer):
- **Request Volume**: 1M requests per second across all services
- **API Endpoints**: 10,000+ different API endpoints
- **Services**: 500+ internal API services
- **Users**: 1M active users (internal + external)
- **Response Time**: Throttling decision in <5ms
- **Availability**: 99.95% uptime for throttling service

### Core Requirements
1. **Low Latency**: Throttling decisions must not add significant latency (<5ms p99)
2. **High Availability**: System continues operating even if throttling service is degraded
3. **Scalability**: Handle growing traffic without linear resource increase
4. **Accuracy**: Minimize false positives while catching actual abuse
5. **Observability**: Comprehensive metrics and alerting for throttling decisions

### Below the line (out of scope)
- Perfect accuracy in distributed counting
- Real-time cross-datacenter synchronization
- Advanced machine learning integration
- Detailed cost optimization

---

## Planning the Approach

**Time Allocation Strategy** (45-60 min interview):
- **Problem Understanding & Requirements**: 5-8 minutes
- **High-Level Design**: 10-15 minutes  
- **Deep Dives** (most critical): 25-35 minutes
- **Additional Considerations**: 5-7 minutes

**Key Decision**: This design will implement a distributed, multi-tiered throttling system with both synchronous (real-time) and asynchronous (sliding window) rate limiting strategies, focusing on system resilience and operational safety.

---

## Core Entities

**Primary Entities**:
- **Throttling Rules**: Configuration defining limits, windows, and actions
- **Rate Counters**: Track request counts across different dimensions
- **Request Context**: Metadata used for throttling decisions (user, IP, endpoint, etc.)
- **Throttling Decision**: Allow/deny/delay result with reasoning

**Key Relationships**:
- Rules define limits for specific dimensions (user, endpoint, global)
- Counters track actual usage against rule limits
- Decisions are made by comparing counters against rules

---

## API Design

*Keep this section brief for staff interviews*

```
# Throttling Service API
POST /throttle/check           # Real-time throttling decision
GET /throttle/rules           # Retrieve throttling rules
PUT /throttle/rules/{id}      # Update throttling rules
GET /throttle/metrics         # Get throttling metrics

# Gateway Integration
X-RateLimit-Remaining: 100    # Headers in responses
X-RateLimit-Reset: 1609459200
X-RateLimit-Retry-After: 60
```

---

## High-Level Design

### Naive Approach & Problems

**Simple Centralized Rate Limiter**:
```
All requests → Single Rate Limiting Service → Redis Counter → Allow/Deny
```

**Problems**:
- **Single Point of Failure**: Centralized service becomes bottleneck and SPOF
- **High Latency**: Every request requires network round-trip to rate limiter
- **Inaccurate Counting**: Race conditions in distributed counter updates
- **No Failover**: When rate limiter fails, either all requests fail or no throttling occurs
- **Resource Intensive**: All throttling logic in one place, poor resource utilization

### Architecture Overview

Our throttling system implements a multi-layered approach with both local and distributed components:

```
[Load Balancer] → [Gateway + Local Throttler] → [API Servers + Local Throttlers] → [Downstream]
                           ↓                              ↓
                    [Distributed Throttling Service]
                           ↓
                    [Rate Counter Storage (Redis)]
```

### Core Components

1. **Local Throttlers (Embedded)**
   - Fast, in-memory rate limiting for immediate decisions
   - Token bucket and sliding window algorithms
   - Embedded in gateways and API servers

2. **Distributed Throttling Service**
   - Centralized rule management and global coordination
   - Handles complex throttling logic and cross-service limits
   - Provides fallback when local throttlers are insufficient

3. **Rate Counter Storage**
   - Distributed Redis cluster for storing rate counters
   - Sliding window counters with time-based partitioning
   - High availability with replication

4. **Rule Management System**
   - Dynamic configuration of throttling rules
   - A/B testing capabilities for gradual rollout
   - Integration with monitoring for automatic adjustments

### Request Flow

1. **Fast Path (Local Throttling)**:
   - Request hits gateway/API server
   - Local throttler checks in-memory limits (token bucket)
   - If within limits, request proceeds immediately
   - If exceeded, consult distributed service

2. **Distributed Path**:
   - Query distributed throttling service
   - Check global/cross-service limits in Redis
   - Make throttling decision based on comprehensive context
   - Cache decision locally for subsequent similar requests

---

## Deep Dives

### 1) How do we handle distributed rate limiting with accuracy?

**Challenge**: Maintaining accurate rate limits across multiple instances without creating bottlenecks or requiring perfect synchronization.

**Solution: Hybrid Local + Distributed Architecture with Approximate Counting**

**Multi-Tier Strategy**:
```
Tier 1: Local Token Buckets (99% of decisions, <1ms latency)
Tier 2: Distributed Sliding Windows (Complex cases, <5ms latency)  
Tier 3: Centralized Coordination (Edge cases, <50ms latency)
```

**Implementation Details**:

**Local Token Bucket (Per Instance)**:
```python
class TokenBucket:
    def __init__(self, capacity, refill_rate):
        self.capacity = capacity
        self.tokens = capacity
        self.refill_rate = refill_rate  # tokens per second
        self.last_refill = time.now()
    
    def try_consume(self, tokens=1):
        self._refill()
        if self.tokens >= tokens:
            self.tokens -= tokens
            return True
        return False
    
    def _refill(self):
        now = time.now()
        elapsed = now - self.last_refill
        tokens_to_add = elapsed * self.refill_rate
        self.tokens = min(self.capacity, self.tokens + tokens_to_add)
        self.last_refill = now
```

**Distributed Sliding Window**:
- Use Redis with time-based keys: `rate_limit:{user_id}:{time_window}`
- Implement sliding window with multiple time buckets
- Accept approximate counting for performance (eventual consistency)

**Coordination Strategy**:
- **Quota Distribution**: Distribute global quotas across instances
- **Periodic Synchronization**: Sync local counters every 10-30 seconds
- **Emergency Coordination**: Real-time sync only for critical violations

**Handling the "Thundering Herd" Problem**:
- **Jittered Sync**: Add randomization to avoid all instances syncing simultaneously
- **Circuit Breaker**: Local fallback when distributed service is overloaded
- **Graceful Degradation**: Looser limits when perfect accuracy isn't available

### 2) How do we implement different throttling strategies for different scenarios?

**Challenge**: Different use cases require different throttling approaches - user quotas, burst handling, system protection, and abuse prevention.

**Solution: Multi-Algorithm Throttling Engine with Policy-Based Selection**

**Algorithm Selection Matrix**:
```
Use Case           | Algorithm        | Characteristics
User API Quotas   | Token Bucket     | Burst allowing, smooth refill
System Protection | Sliding Window   | Precise rate measurement
Burst Handling    | Leaky Bucket     | Traffic shaping, queue-based
DDoS Protection   | Fixed Window     | Simple, aggressive limiting
```

**Throttling Policies**:

1. **Token Bucket for User Quotas**:
   - Allow bursts up to bucket capacity
   - Smooth refill rate for sustained usage
   - Perfect for API rate limits (1000 requests/hour with bursts)

2. **Sliding Window for System Protection**:
   - Precise measurement over time windows
   - Used for protecting downstream services
   - Example: Database connection limits

3. **Adaptive Throttling for Health-Based Limiting**:
```python
class AdaptiveThrottler:
    def __init__(self, base_limit, min_limit, max_limit):
        self.base_limit = base_limit
        self.min_limit = min_limit
        self.max_limit = max_limit
        self.current_limit = base_limit
    
    def adjust_limit(self, system_health_metric):
        # Health metric: 0.0 (unhealthy) to 1.0 (healthy)
        if system_health_metric < 0.3:
            self.current_limit = max(
                self.min_limit, 
                self.current_limit * 0.5
            )
        elif system_health_metric > 0.8:
            self.current_limit = min(
                self.max_limit,
                self.current_limit * 1.2
            )
        
        return self.current_limit
```

**Priority-Based Throttling**:
- **Tiered Limits**: Different limits for different user tiers
- **Request Priority**: Critical requests bypass throttling
- **Graceful Degradation**: Reduce limits for low-priority traffic first

**Multi-Dimensional Throttling**:
- **Per-User**: Individual user rate limits
- **Per-Endpoint**: Specific API endpoint limits
- **Per-IP**: Network-based limiting for security
- **Global**: System-wide capacity protection

### 3) How do we handle throttling decisions at high scale with low latency?

**Challenge**: Making 1M throttling decisions per second with <5ms latency while maintaining accuracy and consistency.

**Solution: Hierarchical Caching with Predictive Pre-computation**

**Latency Optimization Strategy**:
```
L1: In-Memory Cache (Local) - <0.1ms - 90% hit rate
L2: Redis Cluster (Distributed) - <2ms - 9% hit rate  
L3: Database (Persistent) - <50ms - 1% hit rate
```

**Predictive Throttling**:
- **Pre-compute Common Decisions**: Cache throttling decisions for frequent patterns
- **Batch Processing**: Group similar requests for bulk decisions
- **Request Coalescing**: Combine multiple checks into single operations

**Implementation**:
```python
class HierarchicalThrottler:
    def __init__(self):
        self.local_cache = LRUCache(10000)  # Hot decisions
        self.redis_client = RedisCluster()
        self.rule_engine = ThrottlingRuleEngine()
    
    async def should_throttle(self, request_context):
        cache_key = self._generate_cache_key(request_context)
        
        # L1: Check local cache
        cached_decision = self.local_cache.get(cache_key)
        if cached_decision and not cached_decision.is_expired():
            return cached_decision
        
        # L2: Check distributed cache with batch processing
        decisions = await self._batch_check_redis([request_context])
        decision = decisions[0]
        
        # Cache locally for subsequent requests
        self.local_cache.set(cache_key, decision, ttl=30)
        return decision
    
    async def _batch_check_redis(self, contexts):
        # Batch multiple requests into single Redis call
        pipeline = self.redis_client.pipeline()
        keys = [self._generate_redis_key(ctx) for ctx in contexts]
        
        for key in keys:
            pipeline.get(key)
        
        results = await pipeline.execute()
        return self._process_batch_results(results, contexts)
```

**Scale Optimization Techniques**:
- **Request Deduplication**: Avoid duplicate work for identical requests
- **Asynchronous Processing**: Non-blocking throttling checks where possible
- **Connection Pooling**: Maintain persistent connections to Redis
- **Read Replicas**: Distribute read load across multiple Redis instances

### 4) How do we ensure the throttling system doesn't become a single point of failure?

**Challenge**: The throttling system must remain available even when experiencing failures, while still providing protection.

**Solution: Circuit Breaker Pattern with Graceful Degradation**

**Availability Architecture**:
```
Multiple Throttling Service Instances (Active-Active)
     ↓
Redis Cluster (Multi-AZ, Replicated)
     ↓
Local Fallback (When distributed system fails)
```

**Circuit Breaker Implementation**:
```python
class ThrottlingCircuitBreaker:
    def __init__(self, failure_threshold=5, recovery_timeout=60):
        self.failure_count = 0
        self.failure_threshold = failure_threshold
        self.recovery_timeout = recovery_timeout
        self.state = 'CLOSED'  # CLOSED, OPEN, HALF_OPEN
        self.last_failure_time = None
    
    async def call_distributed_throttler(self, request):
        if self.state == 'OPEN':
            if time.now() - self.last_failure_time > self.recovery_timeout:
                self.state = 'HALF_OPEN'
            else:
                return self._local_fallback_decision(request)
        
        try:
            result = await self.distributed_service.check_throttle(request)
            if self.state == 'HALF_OPEN':
                self.state = 'CLOSED'
                self.failure_count = 0
            return result
        except Exception as e:
            self._handle_failure()
            return self._local_fallback_decision(request)
    
    def _local_fallback_decision(self, request):
        # Conservative local decision when distributed system fails
        return self.local_throttler.is_allowed(request)
```

**Graceful Degradation Strategies**:

1. **Local-Only Mode**: When distributed service fails, rely entirely on local throttlers
2. **Conservative Limits**: Apply stricter limits when accuracy is compromised
3. **Fail-Open vs Fail-Closed**: 
   - Fail-open for user experience (allow requests when uncertain)
   - Fail-closed for system protection (deny when system health is poor)

**Data Consistency During Failures**:
- **Eventually Consistent**: Accept temporary inconsistency for availability
- **Conflict Resolution**: When services recover, reconcile counter differences
- **Monitoring**: Alert on significant discrepancies between local and distributed counts

### 5) How do we dynamically adjust throttling rules based on system health?

**Challenge**: Static throttling rules cannot adapt to changing system conditions, leading to either over-throttling (poor UX) or under-throttling (system failures).

**Solution: Adaptive Throttling with Feedback Control Systems**

**Health-Based Adaptive System**:
```python
class AdaptiveThrottlingController:
    def __init__(self):
        self.health_monitor = SystemHealthMonitor()
        self.rule_adjuster = RuleAdjuster()
        self.control_loop_interval = 10  # seconds
    
    async def control_loop(self):
        while True:
            # Gather system health metrics
            health_metrics = await self.health_monitor.get_metrics()
            
            # Calculate adjustment factor
            adjustment = self._calculate_adjustment(health_metrics)
            
            # Apply adjustments to throttling rules
            await self._apply_adjustments(adjustment)
            
            await asyncio.sleep(self.control_loop_interval)
    
    def _calculate_adjustment(self, metrics):
        # Simple PID-like controller
        error = metrics.target_utilization - metrics.current_utilization
        
        # Proportional response
        proportional = error * 0.5
        
        # Integral response (accumulated error)
        self.integral_error += error
        integral = self.integral_error * 0.1
        
        # Derivative response (rate of change)
        derivative = (error - self.previous_error) * 0.2
        self.previous_error = error
        
        adjustment = proportional + integral + derivative
        return max(-0.5, min(0.5, adjustment))  # Clamp adjustment
```

**System Health Indicators**:
- **Response Time**: API latency percentiles (p95, p99)
- **Error Rate**: 5xx error percentage
- **Resource Utilization**: CPU, memory, connection pool usage
- **Queue Depth**: Request queue sizes
- **Downstream Health**: Database/3rd party service health

**Adjustment Strategies**:

1. **Gradual Adjustments**: Small incremental changes to avoid oscillation
2. **Circuit Breaker Integration**: Aggressive throttling when circuit breakers trip
3. **Service Priority**: Adjust different services based on their criticality
4. **User Tier Protection**: Protect premium users from aggressive throttling

**Configuration Management**:
```yaml
adaptive_throttling:
  health_thresholds:
    critical: 0.2    # Aggressive throttling
    warning: 0.5     # Moderate throttling  
    normal: 0.8      # Standard limits
  
  adjustment_factors:
    response_time_weight: 0.4
    error_rate_weight: 0.3
    cpu_utilization_weight: 0.2
    queue_depth_weight: 0.1
  
  rules:
    - service: "user-api"
      base_limit: 1000
      min_limit: 100
      max_limit: 2000
```

**Preventing Control Loop Instability**:
- **Dampening**: Prevent rapid oscillations with smoothing algorithms
- **Dead Band**: Avoid adjustments for small changes
- **Rate Limiting**: Limit frequency of rule changes
- **Manual Override**: Allow operators to disable adaptive behavior

---

## Final Architecture

```
                        Load Balancer
                             |
                    ┌─────────────────┐
                    │    Gateway      │
                    │ + Local Throttle│
                    └─────────────────┘
                             |
        ┌────────────────────┼────────────────────┐
        │                    │                    │
   ┌─────────┐         ┌─────────┐         ┌─────────┐
   │API Svc A│         │API Svc B│         │API Svc C│
   │+ Local  │         │+ Local  │         │+ Local  │
   │Throttle │         │Throttle │         │Throttle │
   └─────────┘         └─────────┘         └─────────┘
        │                    │                    │
        └────────────────────┼────────────────────┘
                             │
                 ┌─────────────────────┐
                 │ Distributed         │
                 │ Throttling Service  │
                 │ (Active-Active)     │
                 └─────────────────────┘
                             |
                    ┌─────────────────┐
                    │ Redis Cluster   │
                    │ (Rate Counters) │
                    └─────────────────┘
                             |
                    ┌─────────────────┐
                    │ Rule Management │
                    │ & Health Monitor│
                    └─────────────────┘
```

---

## Staff-Level Expectations

### Time Management Strategy
- **Problem Scoping** (5 min): Understand cascading failure context and establish throttling requirements
- **Multi-Layer Architecture** (12 min): Present local + distributed hybrid approach with clear separation of concerns
- **Advanced Deep Dives** (30 min): Focus on distributed systems challenges, adaptive algorithms, failure handling
- **Operational Excellence** (8 min): Monitoring, observability, and operational runbooks

### Key Differentiators for Staff Level
1. **Control Systems Theory**: Understanding of PID controllers for adaptive throttling
2. **Distributed Systems Tradeoffs**: CAP theorem applied to rate limiting accuracy vs availability
3. **Failure Mode Analysis**: Comprehensive understanding of how throttling systems can fail
4. **Performance Engineering**: Quantitative analysis of latency and throughput characteristics
5. **Operational Perspective**: Focus on observability, alerting, and incident response

### Expected Follow-up Areas
- **Cross-Service Coordination**: How to handle complex multi-service rate limits
- **Advanced Algorithms**: Machine learning integration for predictive throttling  
- **Security Integration**: Relationship with DDoS protection and abuse detection
- **Cost Optimization**: Resource usage and scaling economics
- **Multi-Region Considerations**: Global rate limiting and data sovereignty

### Demonstrating Staff-Level Thinking
- Proactively address the "distributed counting problem" and accuracy tradeoffs
- Discuss throttling as part of broader system reliability and chaos engineering
- Show awareness of business impact and user experience considerations
- Reference real-world examples (AWS API Gateway, Cloudflare, etc.)
- Think about evolution and migration strategies for existing systems

---

*This design provides a production-ready throttling system that handles the complex requirements of protecting modern distributed architectures while maintaining the flexibility needed for staff-level technical discussions.*