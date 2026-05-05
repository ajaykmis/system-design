// Multi-server WebSocket chat with Redis Pub/Sub fan-out.
//
// Architecture:
//   Client A ──► Server 1 ──PUBLISH──► Redis "chat:room"
//   Client B ──► Server 1                    │
//                                            ├──SUBSCRIBE──► Server 1 → deliver to A, B
//   Client C ──► Server 2                    └──SUBSCRIBE──► Server 2 → deliver to C
//
// Each server:
//   - Maintains a local Hub of only its own connected clients
//   - On incoming client message: PUBLISH to Redis (not local broadcast)
//   - Runs a background goroutine subscribed to Redis
//   - On Redis message: broadcast to all local clients
//
// This means a message from Client A (Server 1) reaches Client C (Server 2)
// via Redis, without Server 1 knowing anything about Server 2's clients.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// ── Message ───────────────────────────────────────────────────────────────────

// ChatMessage is the envelope published to Redis and delivered to clients.
type ChatMessage struct {
	ServerID  string `json:"server_id"` // which server published this (for observability)
	Username  string `json:"username"`
	Text      string `json:"text"`
	Room      string `json:"room"`
	Timestamp int64  `json:"ts"`
}

// ── Hub ───────────────────────────────────────────────────────────────────────
// Manages only the clients connected to THIS server instance.

type client struct {
	conn     *websocket.Conn
	username string
	send     chan []byte // buffered write channel — avoids slow-client blocking
}

type Hub struct {
	mu       sync.RWMutex
	clients  map[*client]struct{}
	serverID string
}

func NewHub(serverID string) *Hub {
	return &Hub{
		clients:  make(map[*client]struct{}),
		serverID: serverID,
	}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	log.Printf("[%s] +%s (%d local)", h.serverID, c.username, h.localCount())
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.send)
	log.Printf("[%s] -%s (%d local)", h.serverID, c.username, h.localCount())
}

func (h *Hub) localCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// localBroadcast delivers a raw JSON payload to all local clients.
// Called by the Redis subscriber goroutine — no Redis involved here.
func (h *Hub) localBroadcast(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- payload:
		default:
			// Client send buffer full — skip rather than block
			log.Printf("[%s] dropped message for slow client %s", h.serverID, c.username)
		}
	}
}

// ── Redis pub/sub ─────────────────────────────────────────────────────────────

func redisChannel(room string) string {
	return "chat:room:" + room
}

// subscribeRedis blocks and forwards every Redis message to localBroadcast.
// Runs in its own goroutine per room.
func subscribeRedis(ctx context.Context, rdb *redis.Client, room string, hub *Hub) {
	ch := rdb.Subscribe(ctx, redisChannel(room)).Channel()
	log.Printf("[%s] subscribed to Redis channel %s", hub.serverID, redisChannel(room))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			hub.localBroadcast([]byte(msg.Payload))
		}
	}
}

// ── Per-client write pump ─────────────────────────────────────────────────────
// Reads from c.send and writes to the WebSocket. Dedicated goroutine per client
// so slow clients never block the hub.

func writePump(c *client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case payload, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			// Ping to detect stale connections
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ── WebSocket handler ─────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func wsHandler(hub *Hub, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.URL.Query().Get("name")
		if username == "" {
			username = "anonymous"
		}
		room := r.URL.Query().Get("room")
		if room == "" {
			room = "general"
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}

		c := &client{
			conn:     conn,
			username: username,
			send:     make(chan []byte, 256),
		}
		hub.register(c)
		defer func() {
			hub.unregister(c)
			conn.Close()
		}()

		// Start the write pump in its own goroutine.
		go writePump(c)

		// Announce join via Redis so ALL servers see it.
		publishSystem(r.Context(), rdb, hub.serverID, room,
			fmt.Sprintf("[%s joined on %s]", username, hub.serverID))

		// Read pump — runs in this goroutine.
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			// Publish to Redis — NOT local broadcast.
			// Redis delivers it back to every server's subscriber, including ours.
			cm := ChatMessage{
				ServerID:  hub.serverID,
				Username:  username,
				Text:      string(msg),
				Room:      room,
				Timestamp: time.Now().UnixMilli(),
			}
			payload, _ := json.Marshal(cm)
			if err := rdb.Publish(r.Context(), redisChannel(room), payload).Err(); err != nil {
				log.Printf("[%s] redis publish error: %v", hub.serverID, err)
			}
		}

		publishSystem(r.Context(), rdb, hub.serverID, room,
			fmt.Sprintf("[%s left]", username))
	}
}

func publishSystem(ctx context.Context, rdb *redis.Client, serverID, room, text string) {
	cm := ChatMessage{
		ServerID:  serverID,
		Username:  "system",
		Text:      text,
		Room:      room,
		Timestamp: time.Now().UnixMilli(),
	}
	payload, _ := json.Marshal(cm)
	rdb.Publish(ctx, redisChannel(room), payload)
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	serverID := os.Getenv("SERVER_ID")
	if serverID == "" {
		serverID = "server-1"
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("redis parse: %v", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}
	log.Printf("[%s] connected to Redis", serverID)

	hub := NewHub(serverID)

	ctx := context.Background()

	// Subscribe to the default "general" room.
	// In production, subscribe per room on first join.
	go subscribeRedis(ctx, rdb, "general", hub)

	http.HandleFunc("/ws", wsHandler(hub, rdb))
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"server_id":     serverID,
			"local_clients": hub.localCount(),
		})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, htmlPage)
	})

	addr := ":8080"
	log.Printf("[%s] listening on %s", serverID, addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ── HTML client ───────────────────────────────────────────────────────────────

const htmlPage = `<!DOCTYPE html>
<html>
<head>
  <title>WebSocket Chat</title>
  <style>
    body { font-family: monospace; max-width: 680px; margin: 40px auto; }
    #log { height: 340px; overflow-y: scroll; border: 1px solid #444;
           padding: 10px; background: #111; color: #0f0; white-space: pre-wrap; }
    #status { font-size: 12px; color: #888; margin: 6px 0; }
    input { width: 72%; padding: 6px; } button { padding: 6px 16px; }
  </style>
</head>
<body>
  <h2>WebSocket Chat (multi-server)</h2>
  <div id="status">connecting...</div>
  <div id="log"></div><br>
  <input id="msg" placeholder="Type a message..." onkeydown="if(event.key==='Enter')send()">
  <button onclick="send()">Send</button>

  <script>
    const name = prompt("Your name:") || "anon";
    const room = "general";
    const ws = new WebSocket("ws://" + location.host + "/ws?name=" + encodeURIComponent(name) + "&room=" + room);
    const log = document.getElementById("log");
    const statusEl = document.getElementById("status");
    let connectedServer = "";

    // Find out which server we landed on
    fetch("/status").then(r => r.json()).then(s => {
      connectedServer = s.server_id;
      statusEl.textContent = "Connected as '" + name + "' on " + s.server_id +
        " | room: " + room;
    });

    ws.onopen    = () => appendLog("--- connected ---");
    ws.onmessage = (e) => {
      const m = JSON.parse(e.data);
      const fromServer = m.server_id !== connectedServer ? " [via " + m.server_id + "]" : "";
      if (m.username === "system") {
        appendLog("  " + m.text);
      } else {
        appendLog(m.username + fromServer + ": " + m.text);
      }
    };
    ws.onclose = () => { appendLog("--- disconnected ---"); statusEl.textContent = "disconnected"; };

    function appendLog(msg) {
      log.textContent += msg + "\n";
      log.scrollTop = log.scrollHeight;
    }
    function send() {
      const input = document.getElementById("msg");
      if (input.value.trim()) { ws.send(input.value); input.value = ""; }
    }
  </script>
</body>
</html>`
