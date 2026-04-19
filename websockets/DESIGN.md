# WebSocket Chat — System Design POC

## What is WebSocket?

WebSocket is a **full-duplex, persistent communication protocol** over a single TCP connection. Unlike HTTP's request-response model, once the WebSocket handshake completes (via an HTTP upgrade), both client and server can **send messages to each other at any time** without re-establishing connections.

```
HTTP:    Client --request-->  Server --response--> Client  (connection closes)
WS:     Client <---messages---> Server              (connection stays open, bidirectional)
```

### The Handshake

WebSocket connections start as a regular HTTP request with an `Upgrade` header:

```
GET /ws?name=alice HTTP/1.1
Host: localhost:8080
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==
Sec-WebSocket-Version: 13
```

The server responds with `101 Switching Protocols`, and the connection transitions from HTTP to a persistent WebSocket.

## When to Use WebSockets

**Good fit:**
- Real-time chat (Slack, WhatsApp Web)
- Live dashboards (stock tickers, monitoring)
- Collaborative editing (Google Docs)
- Multiplayer gaming (state sync)
- Push notifications (alerts without polling)
- Live streaming (comments, reactions)

**Not a good fit:**
- Simple CRUD APIs — REST is simpler
- Infrequent updates — SSE or long-polling suffice
- When you need HTTP caching/CDN benefits

## Architecture

```
┌───────────┐                          ┌───────────────────────────┐
│  Client   │    HTTP GET /ws          │       Go / Python         │
│ (Browser  │ ──────────────────────── │        Server             │
│  or CLI)  │    101 Switching         │                           │
│           │    Protocols             │  ┌─────────────────────┐  │
└───────────┘                          │  │        Hub          │  │
      │                                │  │                     │  │
      │◄──── persistent TCP ──────────►│  │  clients map:       │  │
      │   bidirectional messages       │  │    conn → username  │  │
      │   (no polling, no latency)     │  │                     │  │
      │                                │  │  Register()         │  │
      │                                │  │  Unregister()       │  │
      │                                │  │  Broadcast()        │  │
      │                                │  └─────────────────────┘  │
      │                                └───────────────────────────┘
```

### Components

| Component | Role |
|---|---|
| **Server** | Accepts WebSocket connections, manages the Hub, serves the HTML client |
| **Hub** | Central registry of all connected clients. Handles register, unregister, and fan-out broadcast |
| **Browser Client** | Minimal HTML/JS page served at `/`, connects via `ws://` |
| **CLI Client** | Python asyncio client for terminal-based chat |

### Message Flow

```
1. Alice connects     →  Hub.Register(alice)  →  broadcast "[alice joined]" to all
2. Alice sends "hi"   →  Server reads message →  Hub.Broadcast("alice: hi") to Bob, Charlie...
3. Bob sends "hello"  →  Server reads message →  Hub.Broadcast("bob: hello") to Alice, Charlie...
4. Alice disconnects  →  Hub.Unregister(alice) → broadcast "[alice left]" to all
```

### Key Design Decisions

- **Fan-out broadcast**: The Hub iterates over all connections and writes to each. Simple for a POC; in production, use per-client write goroutines/tasks with buffered channels.
- **No message persistence**: Messages are ephemeral — not stored. A production system would back this with a message queue (Kafka, Redis Pub/Sub).
- **Single-node only**: All state is in-memory. Scaling horizontally requires a shared pub/sub layer (e.g., Redis) so messages reach clients on different servers.

## Project Structure

```
websockets/
├── DESIGN.md      # This file
├── server.go      # Go WebSocket server (gorilla/websocket)
├── server.py      # Python WebSocket server (alternative)
└── client.py      # Python CLI chat client
```

## How to Run

### Prerequisites

```bash
# For the Go server
brew install go                       # if not already installed

# For the Python server/client
pip install websockets
```

### Option A: Go Server

```bash
# Terminal 1 — start the server
go run ./websockets/

# Server logs:
# WebSocket server running on http://localhost:8080
```

### Option B: Python Server

```bash
# Terminal 1 — start the server
python websockets/server.py

# Server logs:
# WebSocket server running on http://localhost:8080
```

### Connecting Clients

**Browser client:**

Open `http://localhost:8080` in one or more browser tabs. You'll be prompted for a name.

**Python CLI client:**

```bash
# Terminal 2
python websockets/client.py --name alice

# Terminal 3
python websockets/client.py --name bob
```

**CLI client options:**

```
--name NAME    Your display name (default: anonymous)
--host HOST    Server host (default: localhost)
--port PORT    Server port (default: 8080)
```

### Example Session

```
# Terminal 2 (alice)
> hello everyone!
bob: hey alice!

# Terminal 3 (bob)
alice: hello everyone!
> hey alice!

# Server logs
+ alice connected (1 online)
+ bob connected (2 online)
alice: hello everyone!
bob: hey alice!
```

## WebSocket vs Alternatives

| Feature | WebSocket | SSE | HTTP Polling |
|---|---|---|---|
| Direction | Bidirectional | Server → Client only | Client → Server only |
| Connection | Persistent | Persistent | New connection each poll |
| Latency | Very low | Low | High (poll interval) |
| Overhead | Minimal after handshake | Minimal | Full HTTP headers each time |
| Browser support | All modern browsers | All modern browsers | Universal |
| Complexity | Medium | Low | Low |

## Production Considerations

For a production-grade system, you would add:

- **Authentication**: Validate tokens during the HTTP upgrade handshake
- **Heartbeat/ping-pong**: Detect stale connections with periodic pings
- **Rate limiting**: Prevent message flooding per client
- **Message persistence**: Store messages in a database for history/replay
- **Horizontal scaling**: Use Redis Pub/Sub or NATS to relay messages across server instances
- **Backpressure**: Per-client write buffers with channel/queue to avoid slow clients blocking the Hub
- **TLS**: Use `wss://` in production (WebSocket over TLS)
