# Design a Book Price Comparison Service

**Ex-FAANG Staff Engineer** | **1 Hour Interview Format**

## 📚 What is our Book Service?

Our book price comparison service allows customers to submit a book, credit card information, and maximum acceptable price. The system searches 50-200 bookstores for the lowest price and either completes the purchase automatically (if price ≤ max) or returns the lowest price found. Think of it as a "buy button" that only triggers when you get a good deal.

---

## Understanding the Problem

### Functional Requirements
**Core Requirements:**
1. Customers submit book + credit card + maximum acceptable price
2. System searches 50-200 bookstores for lowest price in real-time  
3. If lowest price ≤ max price: complete purchase automatically
4. Otherwise: return lowest price found to customer

**Below the line (out of scope):**
- Book recommendations and discovery
- User reviews and ratings
- Multi-currency support
- Order tracking and fulfillment

### Non-Functional Requirements
**Core Requirements:**
1. **Latency**: 10-20 seconds end-to-end response time acceptable
2. **Scale**: Handle 1-2 million unique books, 50-200 bookstore integrations
3. **Availability > Consistency**: Prefer returning stale prices over failing
4. **Rate Limiting**: Respect bookstore API constraints to maintain partnerships

**Below the line (out of scope):**
- Real-time price updates (sub-minute)
- Strong consistency for price data
- High-throughput (system designed for moderate traffic)

---

Here is how your requirements might look on the whiteboard:

**Book Service Requirements**
```
Functional Requirements
- search 50-200 bookstores for lowest price
- auto-purchase if price ≤ threshold
- return lowest price if above threshold

Non-Functional Requirements  
- 10-20s latency acceptable
- availability >> consistency
- respect bookstore rate limits
- scale: 1-2M books, 50-200 stores
```

## The Set Up

### Planning the Approach
We'll follow a structured approach by building our design sequentially through each functional requirement, then use our non-functional requirements to guide deep dive discussions on scaling challenges.

**The Challenge:** We're building a system that must be "polite" to bookstore APIs while providing fast price comparison to users. This creates interesting technical challenges around rate limiting, caching strategies, and resilient API integrations.

### Defining the Core Entities

I like to begin with a broad overview of the primary entities we'll need:

1. **Book**: Represents a book we can purchase, including metadata like ISBN, title, author
2. **User**: Customer submitting purchase requests (minimal - could be anonymous)
3. **Bookstore**: External API integration with rate limits and availability info
4. **Price**: Current and historical price data from each bookstore for cache optimization

**Core Entities**
- Book
- User (minimal)
- Bookstore  
- Price

---

## The API

The API supports our core purchase flow with minimal endpoints:

```javascript
// Submit purchase request - core functionality
POST /purchase
{
  book_isbn,
  max_price,
  payment_info,
  user_info
} 
-> { success: boolean, price_paid?: number, lowest_price_found: number }

// Get current price comparison (optional for transparency)
GET /books/{isbn}/prices 
-> { bookstore: string, price: number, availability: boolean }[]
```

The `POST /purchase` endpoint does all the heavy lifting - price discovery, comparison, and conditional purchase in a single atomic operation.

---

## Data Flow

Before diving into technical design, let's understand how data flows through our system:

1. **User submits purchase request** with book, max price, and payment info
2. **System queries cached prices** for the requested book from multiple bookstores
3. **Background workers refresh stale prices** via rate-limited API calls
4. **Price aggregation service** determines lowest available price
5. **If price ≤ threshold**: Process payment and complete purchase with cheapest bookstore
6. **Otherwise**: Return lowest price found to customer

Note: The "hidden" requirement is efficient price caching - we can't hit 200 bookstore APIs in real-time for every request!

---

## High-Level Design

We'll build our design incrementally, starting with the most fundamental requirement.

**Core Purchase Flow**

Our architecture handles the primary use case: fast price comparison with conditional purchasing.

```
[Client] → [API Gateway] → [Purchase Service] → [Price Cache]
                               ↓                      ↓
                        [Payment Service]    [Background Workers]
                               ↓                      ↓
                          [User/Order DB]      [Bookstore APIs]
```

**Here's what we're building:**

1. **Client**: Web interface or mobile app for purchase requests
2. **API Gateway**: Authentication, rate limiting, request routing  
3. **Purchase Service**: Core business logic - price comparison and conditional purchasing
4. **Price Cache**: Redis cluster storing current prices from all bookstores
5. **Background Workers**: Async price refresh workers respecting rate limits
6. **Payment Service**: Handles transactions with the selected bookstore
7. **Bookstore APIs**: External integrations (Amazon, Barnes & Noble, etc.)

**Key Design Decisions:**
- **Cache-first architecture**: Serve from cache, refresh asynchronously
- **Separation of concerns**: Purchase logic separate from price collection
- **Resilient API integration**: Circuit breakers and fallback strategies

**When a user submits a purchase request:**
1. Client sends POST to `/purchase` with book ISBN and max price
2. Purchase Service checks Price Cache for current bookstore prices
3. If cache hit: Immediately compare prices and process purchase/return result
4. If cache miss/stale: Return best available data + queue background refresh
5. Background workers update prices asynchronously for future requests

---

## Potential Deep Dives

Time for the fun part. We'll take our existing simple design and layer on complexity via our deep dives. These are the **Staff-level discussions** that separate senior+ engineers.

### 1) **How do we efficiently cache and refresh prices from 200 bookstore APIs?** ⭐

Up until now, we've been "black boxing" our price caching, simply assuming we somehow have current prices available. But when we confront the reality of querying 200 different bookstore APIs with various rate limits (typically 1-10 requests/second per API), we realize this is our biggest technical challenge.

We're solving two distinct problems:
- **Cache strategy**: How do we balance freshness vs. response time vs. API quota usage?  
- **Refresh prioritization**: Which books should we update most frequently?

**Good Solution: Time-based TTL Caching**
- Cache all prices with 30-minute TTL
- Background workers refresh expired entries
- Simple but doesn't account for book popularity or price volatility

**Great Solution: Intelligent Multi-tier Caching**
```
L1: Hot books (top 10K) - 5 minute TTL, in-memory cache
L2: Popular books - 15 minute TTL, Redis cluster  
L3: Long tail books - 2 hour TTL, persistent storage
L4: Cold books - 24 hour TTL, database
```

**Prioritization Strategy:**
- Recent user queries increase book priority
- Price volatility affects refresh frequency
- Bookstore availability impacts update scheduling
- Machine learning model predicts "hot" books before they trend

### 2) **How do we handle bookstore API failures and rate limits gracefully?**

With 50-200 different bookstore integrations, we must assume that several will always be experiencing issues. Some will be down, others will be slow, and many will have strict rate limits that change without notice.

**The Challenge**: A single slow bookstore API call can't block the entire purchase flow, but we also can't ignore potentially better prices.

**Good Solution: Circuit Breaker Pattern**
- Track failure rates per bookstore
- "Open" circuit after threshold failures (skip calls)  
- "Half-open" periodically to test recovery
- Exponential backoff for failed stores

**Great Solution: Adaptive Integration Management**
```python
class BookstoreAdapter:
    def __init__(self):
        self.circuit_breaker = CircuitBreaker()
        self.rate_limiter = AdaptiveRateLimiter()  
        self.health_monitor = HealthMonitor()
    
    def get_price(self, isbn):
        if self.circuit_breaker.is_open():
            return None  # Skip this store
            
        if not self.rate_limiter.can_proceed():
            return cached_price  # Use stale data
            
        return self.fetch_with_retry(isbn)
```

**Advanced Features:**
- **Adaptive rate limiting**: Adjust based on API responses and error codes
- **Health monitoring**: Continuous availability checking separate from user requests
- **Graceful degradation**: Return partial results rather than failing completely
- **SLA tracking**: Monitor and report bookstore reliability for business decisions

### 3) **How do we ensure payment processing only happens with accurate prices?**

The Chrome extension approach creates a critical reliability challenge. We're automatically spending users' money based on price data that could be stale, incorrect, or malicious. A pricing error could result in failed purchases, customer complaints, or financial losses.

**Scenario**: Our cache shows a book costs $10, user sets max price of $15, but the actual current price is $25. We attempt purchase and either fail (bad UX) or succeed at wrong price (fraud risk).

**Good Solution: Price Validation Before Payment**
```python
def process_purchase(isbn, max_price, payment_info):
    cached_price = get_cached_price(isbn)
    
    if cached_price <= max_price:
        # Validate with real-time API call before charging
        current_price = bookstore.get_real_time_price(isbn)
        
        if current_price <= max_price:
            return complete_purchase(current_price, payment_info)
        else:
            return {"error": "Price increased, purchase cancelled"}
```

**Great Solution: Multi-source Price Consensus**
- Require 2+ bookstores to confirm similar prices
- Flag outlier prices for manual verification  
- Confidence scoring based on data freshness and source reliability
- Real-time validation for high-value purchases only

**Risk Mitigation:**
- Maximum purchase limits per user/timeframe
- Price change alerts before processing payment
- Automatic refunds for price discrepancies
- Machine learning for anomaly detection

### 4) **How do we optimize for the long tail of rarely searched books?**

Most of our 1-2 million books will be searched infrequently, but when someone does search for an obscure academic textbook, they still expect reasonable performance. We can't afford to keep fresh prices for every book, but we can't provide terrible UX for uncommon requests.

**The Challenge**: 80% of requests are for 20% of books (hot), but the other 80% of books (long tail) still matter for user satisfaction.

**Good Solution: Lazy Loading with Background Refresh**
```python
def get_book_prices(isbn):
    prices = cache.get(isbn)
    
    if not prices:
        # Queue background job, return message to user
        queue_price_refresh.delay(isbn)
        return {"message": "Fetching prices, check back in 30 seconds"}
    
    return prices
```

**Great Solution: Predictive Caching with User Intent**
- Track search patterns and pre-cache related books
- Use browsing data to predict next searches
- Cache warming based on seasonal trends (textbooks in August)  
- Smart cache eviction based on access patterns and book categories

**Advanced Optimization:**
- **Partial results**: Show prices from fast bookstores immediately
- **Progressive enhancement**: Add more prices as they become available
- **User notifications**: Alert when better prices are found after initial search
- **Collaborative filtering**: "Users who searched X also searched Y"

---

## Final Design

Taking a step back, we've designed a scalable system that can intelligently cache bookstore prices, handle API failures gracefully, and safely process automatic purchases while serving both popular and niche books effectively.

```
[Client] → [API Gateway] → [Purchase Service] → [Multi-tier Price Cache]
                               ↓                      ↓
                        [Payment Service]    [Adaptive Background Workers]
                               ↓                      ↓  
                          [Order DB]         [Bookstore API Adapters]
                                                      ↓
                                            [Circuit Breakers & Rate Limiters]
                                                      ↓
                                            [External Bookstore APIs]
```

**Key Components:**
- **Multi-tier caching** with intelligent TTL based on book popularity
- **Circuit breakers** preventing cascade failures from bookstore outages  
- **Adaptive rate limiting** respecting each bookstore's constraints
- **Price validation** ensuring payment accuracy
- **Async processing** for non-blocking price updates

---
┌─────────────────────────────────────────────────────────────────────────────────┐
│                                    CLIENTS                                      │
├─────────────────────┬─────────────────────┬─────────────────────┬─────────────────┤
│   Web App           │   Mobile App        │   Chrome Extension  │   API Partners  │
└─────────────────────┴─────────────────────┴─────────────────────┴─────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                               API GATEWAY                                       │
│  • Rate Limiting        • Authentication       • Request Routing               │
│  • Load Balancing      • SSL Termination      • Request/Response Logging      │
└─────────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            APPLICATION SERVICES                                 │
├─────────────────┬─────────────────┬─────────────────┬─────────────────────────────┤
│  Purchase       │  Price Query    │  Subscription   │  Background Job             │
│  Service        │  Service        │  Service        │  Orchestrator               │
│                 │                 │                 │                             │
│  • Idempotency  │  • Cache Query  │  • User Prefs   │  • Job Scheduling           │
│  • Payment      │  • Price Agg    │  • Alerts       │  • Worker Management        │
│  • Order Mgmt   │  • Chart Data   │  • Notifications│  • Queue Management         │
└─────────────────┴─────────────────┴─────────────────┴─────────────────────────────┘
                                        │
                    ┌───────────────────┼───────────────────┐
                    ▼                   ▼                   ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────┐
│   L0: APP CACHE     │ │   L1: HOT CACHE     │ │   L2: WARM CACHE    │
│                     │ │                     │ │                     │
│ • In-Memory (JVM)   │ │ • Redis Cluster     │ │ • Redis Cluster     │
│ • 10-100MB          │ │ • 1-5GB             │ │ • 10-50GB           │
│ • <1ms latency      │ │ • 1-2ms latency     │ │ • 2-5ms latency     │
│ • 30s-5min TTL      │ │ • 5-15min TTL       │ │ • 15min-2hr TTL     │
│                     │ │                     │ │                     │
│ Hot book prices     │ │ Popular books       │ │ Regular books       │
│ User sessions       │ │ Trending items      │ │ Recent searches     │
│ Rate limit counters │ │ Flash sales         │ │ Category data       │
└─────────────────────┘ └─────────────────────┘ └─────────────────────┘
                                        │
                    ┌───────────────────┼───────────────────┐
                    ▼                   ▼                   ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────┐
│   L3: COLD CACHE    │ │     MESSAGE         │ │   WORKER FLEET      │
│                     │ │     QUEUES          │ │                     │
│ • Redis + Persist   │ │                     │ │ • Auto-scaling      │
│ • 100GB+            │ │ • RabbitMQ/Kafka    │ │ • Specialized       │
│ • 5-10ms latency    │ │ • Priority Queues   │ │ • Circuit Breakers  │
│ • 2-24hr TTL        │ │ • Dead Letters      │ │ • Rate Limited      │
│                     │ │                     │ │                     │
│ All searched books  │ │ Price Refresh Jobs  │ │ Price Fetchers      │
│ Historical data     │ │ Cache Warming       │ │ Cache Warmers       │
│ ML features         │ │ Notification Queue  │ │ Notification Senders│
└─────────────────────┘ └─────────────────────┘ └─────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              PERSISTENT STORAGE                                 │
├─────────────────┬─────────────────┬─────────────────┬─────────────────────────────┤
│   PRIMARY DB    │   PRICES DB     │   ANALYTICS DB  │   SEARCH INDEX              │
│   (PostgreSQL)  │   (TimescaleDB) │   (ClickHouse)  │   (Elasticsearch)           │
│                 │                 │                 │                             │
│ • Users         │ • Current       │ • Cache Metrics │ • Book Search               │
│ • Books         │   Prices        │ • User Behavior │ • Fuzzy Matching            │
│ • Bookstores    │ • Price History │ • API Analytics │ • Autocomplete              │
│ • Orders        │ • Time-series   │ • Business KPIs │ • Category Facets           │
│ • Purchase      │   Optimization  │                 │                             │
│   Requests      │                 │                 │                             │
└─────────────────┴─────────────────┴─────────────────┴─────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         EXTERNAL BOOKSTORE APIS                                │
├─────────────┬─────────────┬─────────────┬─────────────┬─────────────┬──────────┤
│   Amazon    │  Barnes &   │  Book       │   Powell's  │   Better    │   ...    │
│   Books     │  Noble      │  Depository │   Books     │   World     │   200+   │
│             │             │             │             │   Books     │   Total  │
│ Rate: 1/sec │ Rate: 5/sec │ Rate: 2/sec │ Rate: 3/sec │ Rate: 10/sec│          │
│ Uptime: 95% │ Uptime: 98% │ Uptime: 90% │ Uptime: 85% │ Uptime: 99% │          │
└─────────────┴─────────────┴─────────────┴─────────────┴─────────────┴──────────┘
## What is Expected at Each Level?

### Mid-level
I'm looking for candidates who can create a working high-level design addressing the core functional requirements - price comparison and conditional purchasing. You should understand the basic challenge of querying multiple bookstore APIs and propose a simple caching approach. I expect a straightforward database schema and API design. While you might initially suggest synchronous API calls to all bookstores, with guidance you should recognize the latency issues and understand why caching is necessary.

### Senior  
You should quickly identify that API integration and caching are the core technical challenges. I expect you to drive the conversation toward solutions like intelligent caching strategies and circuit breaker patterns. You should understand the trade-offs between different approaches and explain why polling 200 APIs synchronously won't scale. I'm looking for solid understanding of resilient distributed systems - you should explain why we need fallback strategies and how to handle partial failures gracefully.

### Staff+
I'm evaluating your ability to see the bigger picture and balance technical excellence with business constraints. You should proactively recognize that this isn't just a technical problem - it's about managing partnerships with bookstore APIs while delivering user value. I expect you to discuss system evolution: how do we start simple but design for complexity? You should understand business implications like why payment accuracy affects user trust, not just technical correctness. Strong candidates surface concerns I haven't asked about, like handling bookstore policy changes, managing user expectations around stale prices, or planning for peak shopping seasons. You demonstrate systems thinking by considering how caching affects payment accuracy and proposing solutions that balance all constraints.


## Data Flows
USER REQUEST FLOW:
User → API Gateway → Purchase Service → L0 Cache → L1 Cache → L2 Cache → L3 Cache → DB

CACHE POPULATION FLOW:
Background Worker → Bookstore API → Validate → Update DB → Invalidate Caches → Populate Tiers

PRICE UPDATE FLOW:
Bookstore API → Worker → DB Write → Cache Update → Event Publish → Notification Service

CACHE PROMOTION FLOW:
L3 Access → Usage Pattern Analysis → Promote to L2 → High Usage → Promote to L1

FAILURE RECOVERY FLOW:
Bookstore Timeout → Circuit Breaker → Skip Store → Partial Results → Background Retry

## Queue Design Decisions
- How do you handle message ordering? (FIFO vs priority vs parallel processing)
- What's your retry strategy? (Exponential backoff, circuit breakers, dead letters)
- How do you ensure exactly-once processing? (Idempotency keys, database transactions)
- How do you handle backpressure? (When queues fill up faster than workers can process)
- How do you monitor queue health? (Depth, processing time, error rates)

### Business Impact:

Queue latency directly affects user experience (cache miss → slow response)
Queue reliability affects revenue (failed purchase processing)
Queue efficiency affects costs (fewer API calls needed)

The message queues are what allow us to provide fast user responses while doing expensive background work - they're the key to making the whole caching strategy work in practice!
