# WebSocket Chat — Multi-Server Design

## What is WebSocket?

WebSocket is a **full-duplex, persistent communication protocol** over a single TCP connection.
Unlike HTTP's request-response model, once the WebSocket handshake completes (via an HTTP upgrade),
both client and server can send messages at any time without re-establishing connections.

```
HTTP:  Client ──request──► Server ──response──► Client   (connection closes)
WS:    Client ◄────────────────────────────────► Server   (persistent, bidirectional)
```

---

## Architecture

```
                           ┌──────────────────────────────────────────────┐
                           │              nginx (LB :8090)                │
                           │  round-robin, WS-aware (Upgrade headers)     │
                           └───────────────┬──────────────┬───────────────┘
                                           │              │
                             ┌─────────────▼──┐      ┌───▼────────────┐
                             │   Server 1     │      │   Server 2     │
                             │                │      │                │
                             │  Local Hub     │      │  Local Hub     │
                             │  alice ──conn  │      │  bob ───conn   │
                             │  charlie─conn  │      │  dave ──conn   │
                             └───────┬────────┘      └──────┬─────────┘
                                     │  PUBLISH             │  PUBLISH
                                     ▼                      ▼
                             ┌────────────────────────────────────────────┐
                             │           Redis Pub/Sub                    │
                             │      channel: chat:room:general            │
                             └──────────────┬─────────────────────────────┘
                                            │
                            SUBSCRIBE       │       SUBSCRIBE
                    ┌───────────────────────┤───────────────────────┐
                    ▼                       │                       ▼
            Server 1 subscriber            │               Server 2 subscriber
            → localBroadcast()             │               → localBroadcast()
            → delivers to alice, charlie   │               → delivers to bob, dave
```

### Message flow (alice on Server 1 sends to bob on Server 2)

```
1. Alice types "hello"
   → Server 1 reads from WebSocket conn
   → PUBLISH chat:room:general {server_id:"server-1", username:"alice", text:"hello"}

2. Redis fans out to all subscribers
   → Server 1 subscriber receives it → localBroadcast → charlie sees it
   → Server 2 subscriber receives it → localBroadcast → bob, dave see it

3. Alice also sees her own message (Server 1's sub receives its own publish)
```

---

## Key Design Decisions

### 1. Each server only delivers to its own clients

The Hub is **local-only** — `map[*client]struct{}`. A server never holds connections
from other servers. This keeps the broadcast path O(local_clients), not O(all_clients).

```go
// On client message → publish to Redis, NOT local broadcast
rdb.Publish(ctx, "chat:room:general", payload)

// Redis subscriber goroutine → local broadcast only
func subscribeRedis(...) {
    for msg := range sub.Channel() {
        hub.localBroadcast(msg.Payload)  // only this server's clients
    }
}
```

### 2. Per-client write channel (backpressure)

Each client has a buffered `send chan []byte` (capacity 256). The write pump drains it
in its own goroutine. A slow client never blocks the hub's broadcast loop — if the buffer
is full, the message is dropped for that client rather than blocking everyone else.

```
Redis subscriber goroutine
  → hub.localBroadcast()          (holds RLock, fast)
      → for each client: c.send <- payload   (non-blocking select)

Per-client write goroutine
  → reads from c.send
  → conn.WriteMessage()           (can block on slow client, isolated)
```

### 3. Publish on every message, subscribe once per room

The server subscribes to a room channel at startup (or on first join). It publishes every
incoming client message to Redis. Redis delivers to all server subscribers — including the
publishing server itself, so the sender sees their own message echoed back (server-side
confirmation that the message was received and distributed).

### 4. Nginx as WebSocket-aware load balancer

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade    $http_upgrade;
proxy_set_header Connection "Upgrade";
proxy_read_timeout  3600s;   # keep WS connections alive through the proxy
```

WebSocket connections are inherently sticky after the upgrade — the persistent TCP
connection always goes to the same backend. The LB only routes the initial HTTP upgrade.
No session-affinity (`ip_hash`) needed for correctness, only for the upgrade request.

---

## Schema / Envelope

```json
{
  "server_id":  "server-1",
  "username":   "alice",
  "text":       "hello from alice",
  "room":       "general",
  "ts":         1746404800000
}
```

`server_id` is included so clients can display `[via server-2]` annotations when a
message originates from a different server — useful for observability and debugging.

---

## Tradeoffs

### Redis Pub/Sub vs alternatives

| | Redis Pub/Sub | Kafka | NATS | In-process channel |
|---|---|---|---|---|
| **Delivery guarantee** | At-most-once (fire and forget) | At-least-once (with consumer groups) | At-most-once or exactly-once | At-most-once |
| **Message history** | None | Configurable retention | JetStream only | None |
| **Throughput** | ~1M msgs/sec | ~10M msgs/sec | ~10M msgs/sec | unlimited |
| **Ops complexity** | Low (already using Redis) | High (cluster, ZK/KRaft) | Low | Zero |
| **Fan-out model** | Every subscriber gets every message | Consumer groups partition messages | Pub/sub or queue | N/A |
| **Best for** | Chat, notifications, low-stakes fan-out | Event sourcing, audit logs, replay | Low-latency messaging | Single-node only |

**Why Redis Pub/Sub for chat:**
- Messages are ephemeral — chat doesn't need Kafka's replay guarantee
- Every server needs every message (fan-out, not load-balancing)
- Already in the infrastructure — no extra ops burden
- Sub-millisecond fan-out latency

**When to choose Kafka instead:**
- You need message history (user joined mid-conversation, wants to see prior messages)
- You need guaranteed delivery (no dropped messages if a server is briefly down)
- At-most-once is unacceptable

### Single channel vs per-room channels

| | Single global channel | Per-room channel |
|---|---|---|
| Simplicity | ✓ | — |
| Wasted CPU | Servers process messages for rooms they have no clients in | Only receive messages for rooms with local clients |
| Subscription management | Static, at startup | Dynamic, join/leave on connect/disconnect |

**This POC:** single `chat:room:general`. Production: subscribe on first client join to a room, unsubscribe on last client leave. Reduces Redis traffic proportionally to room count.

### At-most-once delivery (the biggest Redis Pub/Sub weakness)

If a server is restarting when Redis publishes a message, that server's clients miss it.
No retry, no replay. Mitigations:

1. **Client-side reconnect + message catch-up**: on reconnect, client fetches last N messages
   from a DB (Cassandra, DynamoDB) that the server writes to alongside publishing to Redis.
2. **Dual-write**: server writes to DB first, then publishes to Redis as a notification.
   Clients on reconnect query `GET /rooms/{room}/messages?after={last_seen_ts}`.
3. **Switch to Kafka**: subscribe from a committed offset, replay on reconnect.

### Connection count limits per server

Each WebSocket is a goroutine (read pump) + a goroutine (write pump) + a file descriptor.
On Linux, default file descriptor limit is 1024 (raises to ~1M with `ulimit -n`).
Memory: ~8KB per goroutine stack × 2 goroutines = ~16KB/connection.

| Clients per server | Memory (goroutines) | FD headroom |
|---|---|---|
| 10,000 | ~160 MB | Fine with `ulimit -n 65535` |
| 100,000 | ~1.6 GB | Fine with `ulimit -n 200000` |
| 1,000,000 | ~16 GB | Need epoll-based server (e.g. gnet, easyws) or Go netpoll |

At Slack/WhatsApp scale, connection servers are separate from application servers.
Connection servers do nothing but maintain TCP state and forward frames. Application
logic is behind an internal API the connection server calls.

### nginx vs L4 load balancer

| | nginx (L7) | AWS NLB / HAProxy L4 |
|---|---|---|
| WebSocket support | ✓ (with upgrade headers) | ✓ (transparent passthrough) |
| SSL termination | ✓ | ✓ |
| Overhead | Parses HTTP frames | Passes raw TCP — lower latency |
| Observability | Access logs, status codes | Limited |
| Sticky sessions | `ip_hash` or cookie | Source IP |

For chat at scale: L4 (NLB) in front of connection servers, L7 (nginx/envoy) for
the internal API layer.

---

## What's Missing (Production Gaps)

| Gap | Fix |
|---|---|
| Message history | Write to Cassandra/DynamoDB on publish; client fetches on reconnect |
| Auth | Validate JWT during HTTP upgrade (before `upgrader.Upgrade()`) |
| At-most-once delivery | Dual-write DB + Redis; client-side catch-up on reconnect |
| Dynamic room subscriptions | Subscribe on first client join; unsubscribe on last leave |
| Rate limiting | Token bucket per connection in the read pump |
| Presence (online/offline) | Redis SET with TTL per user; heartbeat refreshes it |
| Multi-room per client | One subscription goroutine per room the client is in |
| Horizontal Redis | Redis Cluster for >1M msgs/sec; shard by room_id |

---

## Project Structure

```
websockets/
├── DESIGN.md          # This file
├── server.go          # Go multi-server WS server (gorilla/websocket + Redis pub/sub)
├── server.py          # Python single-server alternative
├── client.py          # Python CLI chat client
├── Dockerfile         # Builds server.go
├── docker-compose.yml # 2 servers + Redis + nginx LB
└── nginx.conf         # WS-aware reverse proxy config
```

## How to Run

```bash
cd websockets
docker compose up --build

# Open http://localhost:8090 in two browser tabs
# Tab 1 → nginx routes to server-1 (or server-2)
# Tab 2 → nginx routes to the other server
# Messages cross servers via Redis — annotated with [via server-X] in the UI

# Check which server you're on:
curl http://localhost:8090/status

# Python CLI clients:
python client.py --name alice --port 8090
python client.py --name bob   --port 8090
```

## WebSocket vs Alternatives

| Feature | WebSocket | SSE | HTTP Polling |
|---|---|---|---|
| Direction | Bidirectional | Server → Client only | Client → Server only |
| Connection | Persistent | Persistent | New per poll |
| Latency | Very low | Low | High (poll interval) |
| Overhead | Minimal after handshake | Minimal | Full HTTP headers each time |
| Horizontal scale | Needs pub/sub layer | Needs pub/sub layer | Stateless — scales freely |
