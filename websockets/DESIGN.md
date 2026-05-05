# WebSocket Chat — Multi-Server with Targeted Pub/Sub

## The Problem with Naive Fan-out

The first version subscribed every server to a single global Redis channel:

```
Server 1 ──► SUBSCRIBE chat:room:general
Server 2 ──► SUBSCRIBE chat:room:general
Server 3 ──► SUBSCRIBE chat:room:general   ← gets alice↔bob DM traffic (wrong)
```

This breaks for 1:1 DMs and private channels: **not every server needs every message.**
Server 3 (charlie only, not in alice↔bob DM) has no business processing that traffic.

---

## Architecture

```
                    ┌────────────────────────────────────────────────────────┐
                    │               nginx  :8090  (round-robin)              │
                    └──────────┬──────────────┬────────────────┬─────────────┘
                               │              │                │
                    ┌──────────▼───┐  ┌───────▼──────┐  ┌─────▼────────┐
                    │  Server 1    │  │  Server 2    │  │  Server 3    │
                    │  alice       │  │  bob         │  │  charlie     │
                    └──────┬───────┘  └───────┬──────┘  └──────┬───────┘
                           │                  │                 │
                 SUBSCRIBE  │       SUBSCRIBE  │     SUBSCRIBE   │
                 room:gen   │       room:gen   │     room:gen    │
                 dm:alice:b │       dm:alice:b │                 │
                            │       dm:bob:c   │     dm:bob:c    │
                            │                  │                 │
                            ▼                  ▼                 ▼
                    ┌──────────────────────────────────────────────────────┐
                    │                   Redis Pub/Sub                      │
                    │  room:general    dm:alice:bob    dm:bob:charlie       │
                    └──────────────────────────────────────────────────────┘
```

**Observed subscriptions after alice↔bob DM + #general:**
```
server1: subs = ['dm:alice:bob', 'room:general']
server2: subs = ['dm:alice:bob', 'room:general']
server3: subs = ['room:general']          ← no dm:alice:bob — correct
```

Server 3 never receives alice↔bob DM messages.

---

## Message Flow

### Group channel message (alice → #general)
```
alice types "hello #general!"
  → Server 1 PUBLISH room:general {...}
  → Redis delivers to: Server 1 sub, Server 2 sub, Server 3 sub
  → Server 1 localBroadcast(room:general) → alice
  → Server 2 localBroadcast(room:general) → bob
  → Server 3 localBroadcast(room:general) → charlie
```

### 1:1 DM (alice → bob)
```
alice sends DM to bob
  → Server 1 PUBLISH dm:alice:bob {...}
  → Redis delivers to: Server 1 sub, Server 2 sub   (NOT Server 3)
  → Server 1 localBroadcast(dm:alice:bob) → alice (echo/confirmation)
  → Server 2 localBroadcast(dm:alice:bob) → bob
  → Server 3: not subscribed → zero CPU, zero traffic
```

---

## Client Protocol (JSON over WebSocket)

| Action | Payload | Effect |
|---|---|---|
| `join` | `{channel:"general"}` | Server subscribes to `room:general` if first local client |
| `leave` | `{channel:"general"}` | Server unsubscribes if last local client |
| `msg` | `{channel:"general", text:…}` | PUBLISH to `room:general` |
| `join_dm` | `{to:"bob"}` | Server subscribes to `dm:alice:bob` |
| `dm` | `{to:"bob", text:…}` | PUBLISH to `dm:alice:bob` |

### Redis channel naming
```
Group:  room:{name}              e.g.  room:general
DM:     dm:{min(u1,u2)}:{max}    e.g.  dm:alice:bob   (alphabetically sorted)
```

Alphabetical sort ensures both sides of a DM use the same Redis key regardless of who initiates.

---

## Subscription Manager (reference counting)

```
SubManager:
  subs   map[redisChannel → *redis.PubSub]
  refcnt map[redisChannel → int]

Join(client, channel):
  refcnt[channel]++
  if refcnt == 1:
    ps = rdb.Subscribe(channel)
    go forward(ps)          ← one goroutine per active Redis sub on this server
    log "SUBSCRIBE channel"
  else:
    // reuse existing sub, no new Redis sub needed

Leave(client, channel):
  refcnt[channel]--
  if refcnt == 0:
    ps.Close()              ← stops the forward goroutine
    log "UNSUBSCRIBE channel"

LeaveAll(client):           ← called on disconnect
  for ch in client.channels:
    Leave(client, ch)
```

**Why reference counting matters:**
- Two local clients in #general → 1 Redis subscription (not 2)
- Second client joins → `refcnt` goes 1→2, no new Redis sub
- First client leaves → `refcnt` goes 2→1, still subscribed
- Last client leaves → `refcnt` goes 1→0, UNSUBSCRIBE

---

## Tradeoffs

### Dynamic subscriptions vs static global subscription

| | Static (old) | Dynamic (current) |
|---|---|---|
| Redis subs per server | 1 (always) | 1 per active channel on that server |
| DM privacy | ✗ All servers see all DMs | ✓ Only servers with participants |
| Memory | Fixed | O(active channels on this server) |
| Complexity | Trivial | Subscription manager + refcounting |
| Redis traffic | Every message to every server | Only messages relevant to local clients |
| Cold start | No setup needed | Must re-subscribe on server restart |

At 10M users / 100 active channels per server, dynamic subs reduce Redis bandwidth by ~99% for DMs.

### Redis channel per DM pair vs per user

**Option A — channel per pair (current):** `dm:alice:bob`
- 1 PUBLISH per message
- Subscriber: any server with alice or bob
- Scales to N messages per DM cleanly

**Option B — channel per user:** `user:bob`
- Sender PUBLISHes to `user:bob` (1 publish, direct)
- Bob's server subscribes to `user:bob` (one sub regardless of who's DMing)
- Simpler subscription management (subscribe once per local user, on connect)
- Works well for DMs, but for group channels: sender must PUBLISH to every member's channel (N publishes for N members → fan-out at sender)

| | Per-pair channel | Per-user channel |
|---|---|---|
| DMs | 1 publish | 1 publish |
| Group (N members) | 1 publish | N publishes |
| Sub management | join/leave per conversation | join on connect, leave on disconnect |
| Privacy | Shared between both parties | Each user's channel is private |

**Per-user channel is better for DM-heavy systems (Slack DMs, WhatsApp).
Per-pair/room channel is better for group-heavy systems (IRC, Discord).**

### At-most-once delivery (Redis Pub/Sub limitation)

Redis Pub/Sub drops messages if the subscriber is not connected at publish time (server restarting, network blip). For chat this means:

| Scenario | Impact |
|---|---|
| Server restarts mid-conversation | Messages during restart window are lost |
| Redis failover | Messages during failover are lost |
| Slow subscriber | Messages can be dropped if buffer overflows |

**Mitigation (not in this POC):**
1. **Dual-write**: persist messages to DB (Cassandra/DynamoDB) before publishing to Redis
2. **Client catch-up**: on reconnect, client sends `{last_seen_ts}` and fetches missed messages from DB
3. **Switch to Kafka**: subscribe with committed offsets — on reconnect, replay from last offset

### localBroadcast and per-client channels

```go
// Delivered by the Redis subscriber goroutine
func (h *Hub) localBroadcast(redisChannel string, payload []byte) {
    for c := range h.clients {
        if c.inChannel(redisChannel) {   // only clients in this channel
            c.send <- payload             // non-blocking — drops if buffer full
        }
    }
}
```

A client joined to 3 channels receives only messages for those 3 channels.
A client NOT in a channel receives nothing even if the server is subscribed
(e.g. server subscribed for another local client).

### Backpressure: per-client send buffer

Each client has `send chan []byte` (capacity 256). The write pump is a separate goroutine.
Slow clients (bad connection, high latency) do not block the broadcast loop — their
buffer fills up and messages are dropped rather than stalling delivery to fast clients.
In production: disconnect slow clients after N consecutive drops.

---

## What's Missing (Production Gaps)

| Gap | Fix |
|---|---|
| Message history | Write to DB on publish; client fetches on join/reconnect |
| Auth | Validate JWT before `upgrader.Upgrade()` |
| At-most-once delivery | Dual-write DB + Redis; sequence numbers + client catch-up |
| Read receipts / presence | Per-user Redis key with TTL; heartbeat refreshes it |
| Channel membership enforcement | Check membership DB before subscribing |
| Multi-device per user | Per-user channel (Option B above) so all devices receive |
| Redis failure | Fallback to in-process broadcast for local clients; reconnect loop |
| Horizontal Redis | Redis Cluster sharded by channel name for > 1M msgs/sec |

---

## Proved by test output

```
=== ALICE received ===
  MSG   ch=room:general  alice: hello #general!
  DM    ch=dm:alice:bob  alice: hey bob, private message!

=== BOB received ===
  MSG   ch=room:general  alice: hello #general!
  DM    ch=dm:alice:bob  alice: hey bob, private message!
  DM    ch=dm:bob:charlie  bob: hey charlie!

=== CHARLIE received ===
  MSG   ch=room:general  alice: hello #general!          ← got channel msg ✓
  DM    ch=dm:bob:charlie  bob: hey charlie!             ← got bob DM ✓
                                                         ← no alice↔bob DM ✓
```

---

## How to Run

```bash
cd websockets
docker compose up --build

# Open http://localhost:8090 in 3 tabs, enter different names
# Each tab lands on a different server (round-robin)
# Join #general → all see each other's messages
# Open DM with another user → only those two receive it
# /status shows which Redis channels each server is subscribed to
curl http://localhost:8090/status
```
