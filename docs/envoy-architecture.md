# Envoy Proxy — Architecture from a System Design Perspective

## What is Envoy?

Envoy is a **L3/L4/L7 proxy** designed for large-scale microservice architectures. Built at Lyft, now a CNCF graduated project. It's the data plane behind most service meshes (Istio, AWS App Mesh, Consul Connect).

The key insight: **move networking complexity out of application code and into the infrastructure.**

```
WITHOUT Envoy (every service handles its own concerns):

┌──────────────────────┐     ┌──────────────────────┐
│    Snap Service      │     │    Chat Service      │
│                      │     │                      │
│  ✗ retry logic       │     │  ✗ retry logic       │  ← duplicated
│  ✗ circuit breaker   │     │  ✗ circuit breaker   │  ← in every
│  ✗ rate limiting     │     │  ✗ rate limiting     │  ← service
│  ✗ TLS management    │     │  ✗ TLS management    │  ← in every
│  ✗ load balancing    │     │  ✗ load balancing    │  ← language
│  ✗ observability     │     │  ✗ observability     │
│                      │     │                      │
│  actual logic        │     │  actual logic        │
└──────────────────────┘     └──────────────────────┘


WITH Envoy (sidecar handles all networking):

┌──────────────────────┐     ┌──────────────────────┐
│  ┌───────────────┐   │     │  ┌───────────────┐   │
│  │ Snap Service  │   │     │  │ Chat Service  │   │
│  │               │   │     │  │               │   │
│  │ actual logic  │   │     │  │ actual logic  │   │
│  │ ONLY          │   │     │  │ ONLY          │   │
│  └───────┬───────┘   │     │  └───────┬───────┘   │
│          │localhost   │     │          │localhost   │
│  ┌───────▼───────┐   │     │  ┌───────▼───────┐   │
│  │    Envoy      │   │     │  │    Envoy      │   │
│  │  (sidecar)    │   │     │  │  (sidecar)    │   │
│  │               │   │     │  │               │   │
│  │ retries       │   │     │  │ retries       │   │
│  │ circuit break │   │     │  │ circuit break │   │
│  │ rate limit    │   │     │  │ rate limit    │   │
│  │ mTLS          │   │     │  │ mTLS          │   │
│  │ load balance  │   │     │  │ load balance  │   │
│  │ observability │   │     │  │ observability │   │
│  └───────────────┘   │     │  └───────────────┘   │
└──────────────────────┘     └──────────────────────┘
         Pod A                        Pod B
```

## Core Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        Envoy Process                             │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Listener                               │   │
│  │                    (port 8080)                             │   │
│  │                                                           │   │
│  │  "I accept connections on this port"                      │   │
│  │                                                           │   │
│  │  ┌─────────────────────────────────────────────────────┐  │   │
│  │  │              Filter Chain                           │  │   │
│  │  │                                                     │  │   │
│  │  │  ┌─────────┐  ┌──────────┐  ┌───────────────────┐  │  │   │
│  │  │  │ TLS     │→ │ HTTP     │→ │ Router            │  │  │   │
│  │  │  │Inspector│  │Connection│  │ (matches route    │  │  │   │
│  │  │  │         │  │ Manager  │  │  → picks cluster) │  │  │   │
│  │  │  └─────────┘  └────┬─────┘  └───────────────────┘  │  │   │
│  │  │                    │                                 │  │   │
│  │  │              ┌─────▼──────────────────────┐         │  │   │
│  │  │              │     HTTP Filters            │         │  │   │
│  │  │              │                             │         │  │   │
│  │  │              │  ┌────────┐ ┌─────────────┐ │         │  │   │
│  │  │              │  │Rate    │→│Auth (JWT)   │ │         │  │   │
│  │  │              │  │Limiter │ │             │ │         │  │   │
│  │  │              │  └────────┘ └─────────────┘ │         │  │   │
│  │  │              └─────────────────────────────┘         │  │   │
│  │  └─────────────────────────────────────────────────────┘  │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Clusters                                │   │
│  │                                                           │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │   │
│  │  │ snap-service │  │ chat-service │  │ user-service │    │   │
│  │  │              │  │              │  │              │    │   │
│  │  │ 10.0.1.1:80  │  │ 10.0.2.1:80  │  │ 10.0.3.1:80  │   │   │
│  │  │ 10.0.1.2:80  │  │ 10.0.2.2:80  │  │ 10.0.3.2:80  │   │   │
│  │  │ 10.0.1.3:80  │  │              │  │              │    │   │
│  │  │              │  │ LB: round    │  │ LB: least    │   │   │
│  │  │ LB: least    │  │     robin    │  │     request  │   │   │
│  │  │     request  │  │              │  │              │    │   │
│  │  └──────────────┘  └──────────────┘  └──────────────┘    │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌───────────────────┐                                           │
│  │  Control Plane     │  (xDS APIs — dynamic configuration)      │
│  │  Connection        │  Listener Discovery (LDS)                │
│  │                    │  Route Discovery (RDS)                    │
│  │  Connects to:      │  Cluster Discovery (CDS)                 │
│  │  Istio Pilot /     │  Endpoint Discovery (EDS)                │
│  │  Consul / custom   │  Secret Discovery (SDS)                  │
│  └───────────────────┘                                           │
└─────────────────────────────────────────────────────────────────┘
```

## The Four Core Concepts

| Concept | What it does |
|---------|-------------|
| **Listener** | Binds to a port, accepts connections. "I'm listening on 0.0.0.0:8080" |
| **Filter Chain** | Processes the request through a pipeline: TLS → HTTP parse → rate limit → auth → route. Each filter can modify, reject, or forward. |
| **Route** | Maps incoming request to a cluster. "/snap/*" → snap-service cluster, "/chat/*" → chat-service cluster |
| **Cluster** | A group of upstream endpoints (the actual servers). snap-service = {10.0.1.1, 10.0.1.2, 10.0.1.3}. Includes LB policy, health checks, circuit breaker. |

## Request Lifecycle Through Envoy

```
Client request: POST /snap/send
    │
    ▼
1. LISTENER (port 8080)
    │  Accept TCP connection
    │
    ▼
2. TLS FILTER
    │  Terminate TLS, verify client cert (mTLS)
    │
    ▼
3. HTTP CONNECTION MANAGER
    │  Parse HTTP/1.1 or HTTP/2 frames
    │  Extract headers, path, method
    │
    ▼
4. HTTP FILTERS (pipeline, order matters)
    │
    ├──► Rate Limit Filter
    │    │  Check with external rate limit service
    │    │  429 if exceeded → short-circuit, return to client
    │    │
    ├──► JWT Auth Filter
    │    │  Validate JWT signature + claims
    │    │  401 if invalid → short-circuit
    │    │
    ├──► RBAC Filter
    │    │  Check if this user can access this path
    │    │  403 if denied → short-circuit
    │    │
    └──► Router Filter (always last)
         │  Match route: /snap/* → snap-service cluster
         │
         ▼
5. CLUSTER: snap-service
    │
    │  Load balancing: pick endpoint
    │  ├─ 10.0.1.1 (healthy, 5 active requests)
    │  ├─ 10.0.1.2 (healthy, 3 active requests)  ← selected (least request)
    │  └─ 10.0.1.3 (unhealthy, excluded)
    │
    │  Circuit breaker: check thresholds
    │  ├─ Max connections: 1000 (current: 450 ✓)
    │  ├─ Max pending: 100 (current: 12 ✓)
    │  └─ Max retries: 3
    │
    ▼
6. UPSTREAM CONNECTION
    │  Connect to 10.0.1.2:80
    │  Forward request, stream response back
    │
    │  If 5xx → retry on 10.0.1.1 (retry policy)
    │  If timeout → return 504 to client
    │
    ▼
7. RESPONSE flows back through filters (reverse order)
    │  Add response headers (x-request-id, timing)
    │  Emit metrics (latency, status code)
    │  Emit access log
    │
    ▼
Client receives response
```

## The xDS APIs — Dynamic Configuration

This is Envoy's killer feature. Unlike nginx (static config, requires reload), Envoy discovers its configuration **at runtime** via gRPC streams.

```
┌──────────────────────────────────────────────────────────┐
│                  Control Plane                            │
│           (Istio Pilot / Consul / Custom)                │
│                                                          │
│  Watches: Kubernetes pods, Consul services, DNS, etc.    │
│  Pushes config updates to all Envoy instances via gRPC   │
└──────────┬───────────────────────────────────────────────┘
           │ gRPC streaming (bidirectional)
           │
    ┌──────▼──────────────────────────────────────────┐
    │              xDS Protocol                        │
    │                                                  │
    │  LDS (Listener Discovery)                        │
    │    "Start listening on port 8443 with TLS"       │
    │                                                  │
    │  RDS (Route Discovery)                           │
    │    "Route /snap/v2/* to snap-service-v2 cluster" │
    │                                                  │
    │  CDS (Cluster Discovery)                         │
    │    "New cluster: snap-service-v2, round-robin"   │
    │                                                  │
    │  EDS (Endpoint Discovery)                        │
    │    "snap-service-v2 endpoints: 10.0.5.1, 10.0.5.2│
    │     10.0.5.1 is unhealthy, remove it"            │
    │                                                  │
    │  SDS (Secret Discovery)                          │
    │    "Here's the new TLS cert, rotate now"         │
    │                                                  │
    └─────────────────────────────────────────────────┘

What this enables:
  ─ Zero-downtime config changes (no reload, no restart)
  ─ Canary deployments (shift 5% traffic to v2)
  ─ Auto-scaling (new pod → EDS pushes new endpoint)
  ─ Cert rotation (SDS pushes new cert, zero downtime)
```

## Deployment Patterns

### Pattern 1: Sidecar (Service Mesh — Istio)

Every pod gets its own Envoy. All traffic goes through it.

```
┌─────────────────┐  ┌─────────────────┐
│ Pod A            │  │ Pod B            │
│ ┌─────┐ ┌─────┐ │  │ ┌─────┐ ┌─────┐ │
│ │App A│→│Envoy│─┼──┼─│Envoy│→│App B│ │
│ └─────┘ └─────┘ │  │ └─────┘ └─────┘ │
└─────────────────┘  └─────────────────┘

Use when: Zero-trust networking, per-service observability,
          mTLS everywhere, fine-grained traffic control.
Example:  Snapchat internal service-to-service communication.
```

### Pattern 2: Edge Proxy (API Gateway replacement)

One Envoy cluster at the edge, facing the internet.

```
Internet → [Envoy Edge] → services
                │
                ├──► snap-service
                ├──► chat-service
                └──► user-service

Use when: Replace nginx/HAProxy/Kong as your API gateway.
          Centralized auth, rate limiting, routing.
Example:  Snapchat entry point for mobile client traffic.
```

### Pattern 3: Front Proxy + Sidecar (Full mesh)

Edge Envoy for ingress + sidecar Envoy for internal.

```
Internet → [Envoy Edge]
                │
         ┌──────┼──────┐
         ▼      ▼      ▼
      [Envoy] [Envoy] [Envoy]    ← sidecars
      [Snap]  [Chat]  [User]

Use when: Large orgs where both ingress policy AND
          internal service communication need control.
Example:  Snapchat production architecture (likely this pattern).
```

## Resilience Features

### Circuit Breaker

```
Per-cluster thresholds:
  max_connections: 1000
  max_pending_requests: 100
  max_requests: 2000
  max_retries: 3

When threshold hit → immediate 503, don't even try.
Prevents: cascade failures, resource exhaustion.

Snapchat example:
  Ranking service is slow → snap-service piles up
  connections → circuit breaker trips → return cached
  feed instead of waiting forever.
```

### Outlier Detection

```
"Automatic bad-server removal"

If endpoint returns 5xx 5 times in 30s:
  → eject from load balancing pool for 30s
  → try again after ejection period
  → if still bad, eject for longer (exponential)

Snapchat example:
  One snap-service pod has a memory leak, starts 500ing.
  Envoy detects it, stops sending traffic to that pod.
  Other pods handle the load while ops investigates.
```

### Retry Policy

```
retry_on: 5xx, connect-failure, reset
num_retries: 2
retry_budget: max 20% of active requests

The budget is critical:
  Without it → server is failing → retries double the
  load → server fails harder → retry storm.
  With budget → retries capped at 20% of normal traffic.

Snapchat example:
  Bob opens a snap, request fails → Envoy retries on
  another endpoint transparently. Bob doesn't notice.
  But if half the cluster is down, Envoy won't
  amplify the failure with unlimited retries.
```

## Traffic Splitting — How Canary Deploys Work

```
route_config:
  virtual_hosts:
  - name: snap-service
    routes:
    - match: { prefix: "/snap/" }
      route:
        weighted_clusters:
          clusters:
          - name: snap-v1    weight: 95    ← 95% of traffic
          - name: snap-v2    weight: 5     ← 5% canary

Week 1:  95/5   → monitor error rate, latency
Week 2:  80/20  → still healthy? increase
Week 3:  50/50  → looking good
Week 4:  0/100  → full rollout, remove v1

If v2 has errors at any stage → set weight back to 0/100 (instant rollback)
```

## Observability — What Envoy Gives You For Free

Every request through Envoy emits:

**Metrics (Prometheus/StatsD):**
```
envoy_cluster_upstream_rq_total{cluster="snap-service", response_code="200"}
envoy_cluster_upstream_rq_time{quantile="0.99"}  → P99 latency
envoy_cluster_upstream_cx_active                  → active connections
envoy_cluster_circuit_breakers_cx_open            → circuit breaker state
```

**Access Logs:**
```
timestamp, method, path, response code, upstream host,
time-to-first-byte, total duration, retry count
```

**Distributed Tracing (Jaeger/Zipkin):**
```
Envoy propagates x-request-id across services.
Each sidecar adds a span.

Trace: "Bob opens a snap"

├─ envoy.edge          2ms  (TLS + routing)
├─ envoy.snap-svc      1ms  (sidecar overhead)
│  ├─ snap-service     15ms (business logic)
│  │  ├─ key-store     3ms  (get decryption key)
│  │  └─ blob-store    8ms  (fetch encrypted blob)
│  └─ response         1ms
└─ total              30ms
```

## Envoy vs Alternatives

| | Envoy | nginx | HAProxy | Kong |
|---|---|---|---|---|
| Config reload | Hot (xDS, zero downtime) | Reload (brief interrupt) | Reload | Hot (DB-backed) |
| L7 protocols | HTTP/1, HTTP/2, gRPC, WebSocket, Thrift, Mongo, Redis | HTTP/1, HTTP/2, gRPC | HTTP/1, HTTP/2 | HTTP/1, HTTP/2, gRPC |
| Service mesh | Built for it (sidecar) | Not designed for it | Not designed for it | Possible (Kuma) |
| Observability | Built-in (metrics, tracing, access logs) | Basic access logs | Basic stats | Plugin-based |
| Extension | C++ filters, Lua, WASM | Lua, njs | Lua | Lua, Go plugins |
| Circuit breaking | Built-in, per-cluster | No | No | Plugin |
| Control plane | xDS standard (Istio, Consul) | Static files | Static files | Database |
| Best for | Service mesh, microservices at scale | Web serving, simple reverse proxy | Pure L4/L7 load balancing | API management + developer portal |

## When to Use Envoy — Decision Framework

```
You're running 2-3 services behind nginx
  → No. nginx is simpler and fine.

You have 10+ microservices and need consistent observability
  → Yes. Envoy sidecars give you tracing/metrics everywhere.

You need zero-downtime config changes
  → Yes. xDS hot-reloads without dropping connections.

You need traffic splitting for canary deploys
  → Yes. Weighted clusters are first-class.

You need mTLS between all services
  → Yes. SDS + sidecar pattern handles cert rotation.

You're building a platform for multiple teams
  → Yes. Control plane + xDS lets platform team manage
     networking policy while service teams focus on code.
```

## Mapping to Our Snapchat Architecture

```
Our current Snapchat architecture:

  Gateway (:8080) → does routing + auth + rate limiting

With Envoy, this becomes:

  Envoy Edge (:8080)
    │  ─ TLS termination
    │  ─ JWT auth filter
    │  ─ Rate limit filter (calls external rate-limit service)
    │  ─ Route: /snap/* → snap-service cluster
    │  ─ Route: /chat/* → chat-service cluster
    │
    ├──► [Envoy sidecar] → Snap Service
    │       ─ mTLS to key-store, blob-store
    │       ─ circuit breaker on key-store
    │       ─ retry on blob-store (idempotent GETs)
    │
    ├──► [Envoy sidecar] → Chat Service
    │
    └──► [Envoy sidecar] → Ephemeral Service
            ─ circuit breaker on KMS
            ─ retry policy on blob storage
            ─ outlier detection on reaper workers

Benefit: Snap service code has ZERO networking logic — no retries,
no circuit breakers, no TLS. Envoy handles all of it transparently.
```

## API Gateway vs Load Balancer vs Web Server

For context on how Envoy relates to and replaces these traditional components:

### Load Balancer — "Traffic cop"

What it knows: IP addresses, ports, HTTP paths (L7), server health.
What it does NOT know: Who the user is, what the request means, business rules.

```
Decision: "Server A has 50% CPU, Server B has 90% → send to A"

Algorithms:
  ─ Round robin
  ─ Least connections
  ─ Weighted (bigger server gets more)
  ─ IP hash (sticky sessions)
  ─ Geo-based (US user → us-east)
```

### API Gateway — "Bouncer + receptionist"

What it knows: Who the user is (JWT), rate limits, which service handles which path.
What it does NOT know: Business logic, database queries, domain rules.

```
Responsibilities:
  ─ Authentication (verify JWT)
  ─ Authorization (can this user access this endpoint?)
  ─ Rate limiting (per-user, per-IP, per-endpoint)
  ─ Request routing (/snap/* → snap-service, /chat/* → chat-service)
  ─ Protocol translation (REST → gRPC for internal services)
  ─ Request enrichment (add user-id header from JWT)
  ─ Circuit breaking (if snap-service is down, fail fast)
```

### API/Web Server — "The actual worker"

What it knows: Business logic, domain rules, database schemas.

```
Decision: "Bob wants to open Alice's snap → check state machine,
           decrypt blob, increment view count, start timer"
```

### Why They Separate at Scale

```
1. DIFFERENT SCALING NEEDS
   ─ LB: needs to handle 1M connections, doesn't need much CPU
   ─ Gateway: needs CPU for JWT validation, rate limit lookups
   ─ Services: need CPU + memory for business logic + DB

2. DIFFERENT TEAMS OWN THEM
   ─ LB: infra/platform team
   ─ Gateway: platform engineering team
   ─ Services: product teams (chat team, snap team, stories team)

3. DIFFERENT FAILURE MODES
   ─ LB fails: nothing works (must be highly redundant)
   ─ Gateway fails: auth breaks, but you can bypass in emergencies
   ─ One service fails: only that feature breaks, others are fine
```

### Where Companies Land

| Company | LB | API Gateway | Services |
|---|---|---|---|
| **Snapchat** | Google Cloud LB | Custom (Envoy-based) | Go/Java microservices |
| **Netflix** | AWS ELB | Zuul → Spring Cloud Gateway | Java microservices |
| **Uber** | Custom L4 | Custom (Karken) | Go microservices |
| **Stripe** | AWS ALB | Custom (Ruby) | Ruby/Go services |

The fundamental system design insight: **Envoy moves cross-cutting concerns (auth, retries, observability, TLS, traffic shaping) from application code into infrastructure.** This is why it became the standard data plane for microservice architectures — it lets service teams write business logic while platform teams manage networking policy.

## Rate Limiting at the Edge — How Cloud API Gateways Handle It

At companies like Snapchat (GCP) or Netflix (AWS), rate limiting at the API gateway involves **three layers** working together. The key question is: where does the counter live, and who enforces it?

### The Three Layers

```
┌─────────────────────────────────────────────────────────────────────┐
│  LAYER 1: Cloud Provider's Built-in Rate Limiting                   │
│           (GCP Cloud Armor / AWS WAF / API Gateway throttling)      │
│                                                                      │
│  What it does:                                                       │
│    ─ Blunt per-IP or per-region limits (DDoS protection)             │
│    ─ "No single IP sends more than 10K req/sec"                      │
│    ─ Runs at the cloud edge, before your code                        │
│                                                                      │
│  How it works:                                                       │
│    ─ Counters in the cloud provider's infra (you don't manage them)  │
│    ─ Rules configured via console/IaC                                │
│    ─ Enforced at the load balancer layer                             │
│                                                                      │
│  Snapchat on GCP:                                                    │
│    Cloud Armor rate limiting rules:                                   │
│      ─ 10K req/sec per IP (anti-DDoS)                                │
│      ─ Geo-blocking for sanctioned regions                           │
│      ─ Bot detection via reCAPTCHA Enterprise                        │
│    This is NOT per-user — it's per-IP, per-region, per-path          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ passes through
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│  LAYER 2: Envoy Rate Limit Filter → External Rate Limit Service     │
│           (This is where per-user, per-endpoint limiting happens)    │
│                                                                      │
│  Envoy does NOT count requests itself.                               │
│  It calls an external rate limit service via gRPC.                   │
│                                                                      │
│  The flow:                                                           │
│                                                                      │
│  Request arrives at Envoy                                            │
│       │                                                              │
│       ▼                                                              │
│  Rate Limit Filter builds a descriptor:                              │
│    {                                                                 │
│      "domain": "snap-api",                                           │
│      "descriptors": [                                                │
│        {"key": "user_id",  "value": "user_abc123"},                  │
│        {"key": "endpoint", "value": "/snap/send"}                    │
│      ]                                                               │
│    }                                                                 │
│       │                                                              │
│       │  gRPC call (sub-millisecond, within the same DC)             │
│       ▼                                                              │
│  ┌──────────────────────────────────────────────┐                    │
│  │     External Rate Limit Service              │                    │
│  │     (Lyft's ratelimit / custom Go service)   │                    │
│  │                                              │                    │
│  │  1. Receive descriptor {user_id, endpoint}   │                    │
│  │  2. Look up policy:                          │                    │
│  │     "user_id + /snap/send → 100 req/min"     │                    │
│  │  3. Check counter in Redis:                  │                    │
│  │     INCR ratelimit:user_abc123:/snap/send    │                    │
│  │     EXPIRE key 60                            │                    │
│  │  4. Return: OVER_LIMIT or OK                 │                    │
│  │                                              │                    │
│  │  Redis is the counter store:                 │                    │
│  │    ─ Shared across all Envoy instances        │                    │
│  │    ─ Atomic INCR (no race conditions)         │                    │
│  │    ─ TTL-based window expiry                  │                    │
│  └──────────────┬───────────────────────────────┘                    │
│                 │                                                     │
│                 ▼                                                     │
│  Envoy receives response:                                            │
│    OK         → continue to upstream service                         │
│    OVER_LIMIT → return 429 to client, never hits upstream            │
└──────────────────────────────────────────────────────────────────────┘
                               │ passes through
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│  LAYER 3: Application-Level Rate Limiting                            │
│           (Service-specific, business logic limits)                   │
│                                                                       │
│  Examples at Snapchat:                                                │
│    ─ SMS verification: max 3 codes per phone per hour                 │
│    ─ Story posting: max 100 stories per day per user                  │
│    ─ Friend requests: max 50 per day                                  │
│                                                                       │
│  These are too business-specific for the gateway.                     │
│  Enforced inside the service itself.                                  │
└──────────────────────────────────────────────────────────────────────┘
```

### Why Envoy Calls an External Service (Not Inline)

```
Option A: Count in Envoy's memory (local rate limiting)
  ─ Fast, no network hop
  ─ BUT: each Envoy instance has its own counter
  ─ With 50 Envoy instances, user gets 50x the actual limit
  ─ Only useful for per-instance protection (connection limits)

Option B: Call external service backed by Redis (global rate limiting)
  ─ One extra gRPC hop (~1ms within same DC)
  ─ BUT: single source of truth across ALL Envoy instances
  ─ User's counter is shared: 100 req/min means 100 total, not per-instance
  ─ This is what production systems use

Envoy actually supports BOTH simultaneously:

  ┌─────────────────────────────────────────────┐
  │  Envoy Filter Chain                          │
  │                                              │
  │  ┌──────────────────┐  ┌──────────────────┐  │
  │  │ Local Rate Limit │→ │Global Rate Limit │  │
  │  │ (in-memory)      │  │(external service)│  │
  │  │                  │  │                  │  │
  │  │ "Max 1000 conn   │  │ "Max 100 req/min │  │
  │  │  per instance"   │  │  per user global" │  │
  │  │                  │  │                  │  │
  │  │ First line of    │  │ Accurate global  │  │
  │  │ defense (fast)   │  │ enforcement      │  │
  │  └──────────────────┘  └──────────────────┘  │
  └─────────────────────────────────────────────┘
```

### The External Rate Limit Service — Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                                                              │
│  Envoy         Envoy         Envoy         Envoy             │
│  instance 1    instance 2    instance 3    instance N        │
│    │              │              │              │            │
│    │   gRPC       │   gRPC       │   gRPC       │   gRPC    │
│    └──────┬───────┴──────┬───────┴──────┬───────┘           │
│           │              │              │                    │
│           ▼              ▼              ▼                    │
│    ┌─────────────────────────────────────────────┐          │
│    │      Rate Limit Service (Go)                │          │
│    │      (Lyft open-source / custom)            │          │
│    │                                             │          │
│    │  ┌───────────────────────────────────────┐  │          │
│    │  │ Config (YAML or dynamic):             │  │          │
│    │  │                                       │  │          │
│    │  │ domain: snap-api                      │  │          │
│    │  │ descriptors:                          │  │          │
│    │  │   - key: user_id                      │  │          │
│    │  │     descriptors:                      │  │          │
│    │  │       - key: endpoint                 │  │          │
│    │  │         value: /snap/send             │  │          │
│    │  │         rate_limit:                   │  │          │
│    │  │           unit: minute                │  │          │
│    │  │           requests_per_unit: 100      │  │          │
│    │  │                                       │  │          │
│    │  │       - key: endpoint                 │  │          │
│    │  │         value: /snap/open             │  │          │
│    │  │         rate_limit:                   │  │          │
│    │  │           unit: minute                │  │          │
│    │  │           requests_per_unit: 200      │  │          │
│    │  │                                       │  │          │
│    │  │   - key: ip_address                   │  │          │
│    │  │     rate_limit:                       │  │          │
│    │  │       unit: second                    │  │          │
│    │  │       requests_per_unit: 50           │  │          │
│    │  └───────────────────────────────────────┘  │          │
│    │                                             │          │
│    │  Pipeline:                                  │          │
│    │    1. Receive descriptor from Envoy          │          │
│    │    2. Match against config → find limit      │          │
│    │    3. INCR counter in Redis                  │          │
│    │    4. Compare counter vs limit               │          │
│    │    5. Return OK or OVER_LIMIT                │          │
│    └─────────────────┬───────────────────────────┘          │
│                      │                                       │
│                      ▼                                       │
│    ┌─────────────────────────────────────────────┐          │
│    │            Redis Cluster                     │          │
│    │                                             │          │
│    │  Key format:                                │          │
│    │    ratelimit:{domain}:{descriptors}:{window} │          │
│    │                                             │          │
│    │  Example:                                   │          │
│    │    ratelimit:snap-api:user_abc123:           │          │
│    │      /snap/send:1713500000  →  47            │          │
│    │    TTL: 60s (auto-expires with the window)   │          │
│    │                                             │          │
│    │  Operations:                                │          │
│    │    INCR (atomic, no race conditions)         │          │
│    │    EXPIRE (window cleanup)                   │          │
│    │    Pipeline batching (multiple descriptors)  │          │
│    └─────────────────────────────────────────────┘          │
└─────────────────────────────────────────────────────────────┘
```

### GCP vs AWS — How Cloud Providers Differ

```
┌──────────────────────┬───────────────────────┬──────────────────────┐
│                      │  GCP (Snapchat)       │  AWS (Netflix)       │
├──────────────────────┼───────────────────────┼──────────────────────┤
│ Edge DDoS            │ Cloud Armor           │ AWS Shield + WAF     │
│ protection           │ (per-IP, geo, bot)    │ (per-IP, geo, bot)   │
│                      │                       │                      │
│ L7 Load Balancer     │ Google Cloud LB       │ ALB / API Gateway    │
│                      │ (global, anycast)     │ (regional)           │
│                      │                       │                      │
│ Built-in rate        │ Cloud Armor rate      │ WAF rate-based rules │
│ limiting             │ limiting rules        │ API GW throttling    │
│                      │ (per-IP, per-path)    │ (per-API-key, stage) │
│                      │                       │                      │
│ Per-user rate        │ Envoy + external      │ Envoy + external     │
│ limiting             │ service + Redis       │ service + Redis      │
│ (custom)             │ (Memorystore)         │ (ElastiCache)        │
│                      │                       │                      │
│ Key difference       │ GCP's global LB is    │ AWS API Gateway has  │
│                      │ anycast (single IP,   │ built-in per-key     │
│                      │ routes globally) —    │ throttling, but orgs │
│                      │ but rate limiting is  │ at Netflix's scale   │
│                      │ basic. Custom Envoy   │ outgrow it and use   │
│                      │ layer needed for      │ Zuul/Envoy instead.  │
│                      │ per-user limits.      │                      │
└──────────────────────┴───────────────────────┴──────────────────────┘
```

### What Happens When the Rate Limit Service Goes Down?

```
This is a critical design decision. Options:

FAIL OPEN (allow all traffic):
  ─ Envoy config: failure_mode_deny: false
  ─ If rate limit service is down → all requests pass through
  ─ Risk: no protection during outage
  ─ Used by: most companies (availability > protection)

FAIL CLOSED (deny all traffic):
  ─ Envoy config: failure_mode_deny: true
  ─ If rate limit service is down → all requests rejected (429)
  ─ Risk: rate limit outage = total outage
  ─ Used by: almost nobody at the global level

HYBRID (what Snapchat likely does):
  ─ Global rate limit: fail open (don't break the app)
  ─ Local rate limit: always on (in-memory, no dependency)
  ─ Critical endpoints (payments, SMS): fail closed
  ─ Circuit breaker on the rate limit service itself
    (if it's slow, stop calling it rather than adding latency)

  ┌──────────────────────────────────────────────┐
  │  Envoy Rate Limit Filter config:              │
  │                                               │
  │  rate_limit_service:                          │
  │    grpc_service:                              │
  │      timeout: 20ms        ← very aggressive  │
  │    failure_mode_deny: false  ← fail open     │
  │                                               │
  │  If gRPC call takes >20ms → skip rate limit,  │
  │  allow request through. The rate limit service │
  │  must be FAST or it gets bypassed.             │
  └──────────────────────────────────────────────┘
```

### Rate Limiting Decision Summary

```
"Where should I rate limit?"

┌─────────────────┬────────────────────────────┬────────────────────┐
│ What            │ Where                      │ Counter store      │
├─────────────────┼────────────────────────────┼────────────────────┤
│ DDoS / per-IP   │ Cloud provider edge        │ Cloud managed      │
│                 │ (Cloud Armor / WAF)        │ (you don't see it) │
│                 │                            │                    │
│ Per-user API    │ Envoy → external service   │ Redis              │
│ limits          │ (global, accurate)         │ (shared cluster)   │
│                 │                            │                    │
│ Per-instance    │ Envoy local rate limit     │ In-memory           │
│ protection      │ (fast, no dependency)      │ (per Envoy)        │
│                 │                            │                    │
│ Business rules  │ Application code           │ Redis or DB        │
│ (3 SMS/hr)      │ (service-level)            │                    │
└─────────────────┴────────────────────────────┴────────────────────┘
```
