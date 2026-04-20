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
