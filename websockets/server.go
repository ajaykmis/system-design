// Multi-server WebSocket chat with targeted Redis Pub/Sub routing.
//
// Key insight: not every server needs every message.
//   - 1:1 DM (alice↔bob): only servers hosting alice or bob subscribe to dm:alice:bob
//   - Channel #general: only servers with members in #general subscribe to room:general
//   - Server 3 (charlie only, not in alice↔bob DM): zero Redis traffic from that DM
//
// Per-server subscription lifecycle:
//   first local client joins channel → SUBSCRIBE to Redis channel
//   last local client leaves channel → UNSUBSCRIBE from Redis channel
//
// Client protocol (JSON over WebSocket):
//   {"action":"join",     "channel":"general"}          join group channel
//   {"action":"leave",    "channel":"general"}          leave group channel
//   {"action":"msg",      "channel":"general","text":…} send to channel
//   {"action":"join_dm",  "to":"bob"}                   open DM with user
//   {"action":"dm",       "to":"bob","text":…}          send direct message
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// ── Protocol types ────────────────────────────────────────────────────────────

// ClientMsg is sent from the browser/client to the server.
type ClientMsg struct {
	Action  string `json:"action"`  // join | leave | msg | join_dm | dm
	Channel string `json:"channel"` // group channel name (no #)
	To      string `json:"to"`      // DM recipient
	Text    string `json:"text"`
}

// Envelope is published to Redis and forwarded to clients as-is.
type Envelope struct {
	Type      string `json:"type"`     // msg | dm | system
	From      string `json:"from"`
	Channel   string `json:"channel"`  // Redis channel key (e.g. room:general, dm:alice:bob)
	Text      string `json:"text"`
	ServerID  string `json:"server_id"`
	Timestamp int64  `json:"ts"`
}

// ── Redis channel helpers ─────────────────────────────────────────────────────

func roomChannel(name string) string { return "room:" + name }

// dmChannel returns the canonical Redis key for a 1:1 DM.
// Alphabetically sorted so alice→bob and bob→alice use the same key.
func dmChannel(u1, u2 string) string {
	pair := []string{u1, u2}
	sort.Strings(pair)
	return "dm:" + pair[0] + ":" + pair[1]
}

// ── Client ────────────────────────────────────────────────────────────────────

type client struct {
	conn     *websocket.Conn
	username string
	send     chan []byte // buffered — write pump drains this

	mu       sync.RWMutex
	channels map[string]struct{} // Redis channels this client is currently in
}

func newClient(conn *websocket.Conn, username string) *client {
	return &client{
		conn:     conn,
		username: username,
		send:     make(chan []byte, 256),
		channels: make(map[string]struct{}),
	}
}

func (c *client) inChannel(ch string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.channels[ch]
	return ok
}

func (c *client) joinCh(ch string) {
	c.mu.Lock()
	c.channels[ch] = struct{}{}
	c.mu.Unlock()
}

func (c *client) leaveCh(ch string) {
	c.mu.Lock()
	delete(c.channels, ch)
	c.mu.Unlock()
}

func (c *client) allChannels() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.channels))
	for ch := range c.channels {
		out = append(out, ch)
	}
	return out
}

// ── Hub ───────────────────────────────────────────────────────────────────────
// Local-only: only clients connected to THIS server instance.

type Hub struct {
	mu       sync.RWMutex
	clients  map[*client]struct{}
	serverID string
}

func NewHub(serverID string) *Hub {
	return &Hub{clients: make(map[*client]struct{}), serverID: serverID}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	log.Printf("[%s] +%s (%d local)", h.serverID, c.username, h.count())
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.send)
	log.Printf("[%s] -%s (%d local)", h.serverID, c.username, h.count())
}

func (h *Hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// localBroadcast delivers to all local clients that are in redisChannel.
// Called by the SubManager's forward goroutine — no publish to Redis here.
func (h *Hub) localBroadcast(redisChannel string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.inChannel(redisChannel) {
			select {
			case c.send <- payload:
			default:
				log.Printf("[%s] slow client %s — dropped", h.serverID, c.username)
			}
		}
	}
}

// ── SubManager ────────────────────────────────────────────────────────────────
// Manages per-server Redis Pub/Sub subscriptions with reference counting.
//
//   refcnt[ch] == 0  → not subscribed to Redis
//   refcnt[ch] == 1  → 1 local client in ch  → 1 Redis subscription
//   refcnt[ch] == 2  → 2 local clients in ch → still only 1 Redis subscription
//   refcnt[ch] == 0  (after leaving) → unsubscribe from Redis

type SubManager struct {
	mu     sync.Mutex
	subs   map[string]*redis.PubSub // redisChannel → active PubSub
	refcnt map[string]int
	rdb    *redis.Client
	hub    *Hub
}

func NewSubManager(rdb *redis.Client, hub *Hub) *SubManager {
	return &SubManager{
		subs:   make(map[string]*redis.PubSub),
		refcnt: make(map[string]int),
		rdb:    rdb,
		hub:    hub,
	}
}

func (sm *SubManager) Join(c *client, redisChannel string) {
	if c.inChannel(redisChannel) {
		return
	}
	c.joinCh(redisChannel)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refcnt[redisChannel]++
	if sm.refcnt[redisChannel] == 1 {
		// First local client in this channel — subscribe to Redis now.
		ps := sm.rdb.Subscribe(context.Background(), redisChannel)
		sm.subs[redisChannel] = ps
		go sm.forward(ps, redisChannel)
		log.Printf("[%s] SUBSCRIBE %s (refcnt=1)", sm.hub.serverID, redisChannel)
	} else {
		log.Printf("[%s] JOIN %s (refcnt=%d, no new Redis sub)",
			sm.hub.serverID, redisChannel, sm.refcnt[redisChannel])
	}
}

func (sm *SubManager) Leave(c *client, redisChannel string) {
	if !c.inChannel(redisChannel) {
		return
	}
	c.leaveCh(redisChannel)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refcnt[redisChannel]--
	if sm.refcnt[redisChannel] == 0 {
		// Last local client left — unsubscribe from Redis.
		if ps, ok := sm.subs[redisChannel]; ok {
			ps.Close() // stops the forward goroutine
			delete(sm.subs, redisChannel)
		}
		delete(sm.refcnt, redisChannel)
		log.Printf("[%s] UNSUBSCRIBE %s (last client left)", sm.hub.serverID, redisChannel)
	} else {
		log.Printf("[%s] LEAVE %s (refcnt=%d)", sm.hub.serverID, redisChannel, sm.refcnt[redisChannel])
	}
}

// LeaveAll cleans up all subscriptions for a disconnecting client.
func (sm *SubManager) LeaveAll(c *client) {
	for _, ch := range c.allChannels() {
		sm.Leave(c, ch)
	}
}

// forward pumps Redis messages into localBroadcast.
func (sm *SubManager) forward(ps *redis.PubSub, redisChannel string) {
	for msg := range ps.Channel() {
		sm.hub.localBroadcast(redisChannel, []byte(msg.Payload))
	}
}

func (sm *SubManager) activeSubs() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]string, 0, len(sm.subs))
	for ch := range sm.subs {
		out = append(out, ch)
	}
	return out
}

// ── Write pump ────────────────────────────────────────────────────────────────

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
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func publish(rdb *redis.Client, ch string, env Envelope) {
	payload, _ := json.Marshal(env)
	if err := rdb.Publish(context.Background(), ch, payload).Err(); err != nil {
		log.Printf("publish error %s: %v", ch, err)
	}
}

func systemMsg(serverID, ch, text string) Envelope {
	return Envelope{Type: "system", Channel: ch, Text: text,
		ServerID: serverID, Timestamp: time.Now().UnixMilli()}
}

func sendDirect(c *client, env Envelope) {
	payload, _ := json.Marshal(env)
	select {
	case c.send <- payload:
	default:
	}
}

// ── WebSocket handler ─────────────────────────────────────────────────────────

func wsHandler(hub *Hub, rdb *redis.Client, subMgr *SubManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.URL.Query().Get("name")
		if username == "" {
			username = "anon"
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade: %v", err)
			return
		}

		c := newClient(conn, username)
		hub.register(c)
		defer func() {
			subMgr.LeaveAll(c)
			hub.unregister(c)
			conn.Close()
		}()

		go writePump(c)

		// Direct welcome — not via Redis (only this client needs it)
		sendDirect(c, systemMsg(hub.serverID, "",
			fmt.Sprintf("Welcome %s! You are on %s. Join channels with {action:join, channel:X} or open a DM with {action:join_dm, to:Y}",
				username, hub.serverID)))

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			var msg ClientMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}

			switch msg.Action {

			case "join":
				// Subscribe this server to room:X if not already.
				ch := roomChannel(msg.Channel)
				subMgr.Join(c, ch)
				publish(rdb, ch, systemMsg(hub.serverID, ch,
					fmt.Sprintf("[%s joined #%s on %s]", username, msg.Channel, hub.serverID)))

			case "leave":
				ch := roomChannel(msg.Channel)
				publish(rdb, ch, systemMsg(hub.serverID, ch,
					fmt.Sprintf("[%s left #%s]", username, msg.Channel)))
				subMgr.Leave(c, ch)

			case "msg":
				ch := roomChannel(msg.Channel)
				if !c.inChannel(ch) {
					sendDirect(c, systemMsg(hub.serverID, "",
						fmt.Sprintf("Not in #%s — send {action:join, channel:%s} first", msg.Channel, msg.Channel)))
					continue
				}
				publish(rdb, ch, Envelope{
					Type: "msg", From: username, Channel: ch,
					Text: msg.Text, ServerID: hub.serverID,
					Timestamp: time.Now().UnixMilli(),
				})

			case "join_dm":
				// Server subscribes to dm:X:Y — only when a local participant joins.
				ch := dmChannel(username, msg.To)
				subMgr.Join(c, ch)
				sendDirect(c, systemMsg(hub.serverID, ch,
					fmt.Sprintf("DM with %s ready (channel: %s)", msg.To, ch)))

			case "dm":
				ch := dmChannel(username, msg.To)
				if !c.inChannel(ch) {
					// Auto-join on first send
					subMgr.Join(c, ch)
				}
				publish(rdb, ch, Envelope{
					Type: "dm", From: username, Channel: ch,
					Text: msg.Text, ServerID: hub.serverID,
					Timestamp: time.Now().UnixMilli(),
				})
			}
		}
	}
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

	opt, _ := redis.ParseURL(redisURL)
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("redis: %v", err)
	}
	log.Printf("[%s] connected to Redis", serverID)

	hub := NewHub(serverID)
	subMgr := NewSubManager(rdb, hub)

	http.HandleFunc("/ws", wsHandler(hub, rdb, subMgr))
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"server_id":     serverID,
			"local_clients": hub.count(),
			"subscriptions": subMgr.activeSubs(), // only what this server actually needs
		})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, htmlPage)
	})

	log.Printf("[%s] listening on :8080", serverID)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ── HTML client ───────────────────────────────────────────────────────────────

const htmlPage = `<!DOCTYPE html>
<html>
<head>
<title>Chat</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: monospace; display: flex; height: 100vh; background: #1a1a1a; color: #ccc; }
#sidebar { width: 200px; background: #111; padding: 12px; border-right: 1px solid #333; display: flex; flex-direction: column; gap: 12px; }
#sidebar h3 { color: #888; font-size: 11px; text-transform: uppercase; }
.ch-item { padding: 4px 8px; cursor: pointer; border-radius: 4px; font-size: 13px; }
.ch-item:hover, .ch-item.active { background: #2a2a2a; color: #fff; }
#main { flex: 1; display: flex; flex-direction: column; }
#top-bar { padding: 10px 16px; border-bottom: 1px solid #333; font-size: 13px; color: #888; }
#messages { flex: 1; overflow-y: auto; padding: 12px 16px; display: flex; flex-direction: column; gap: 4px; }
.msg { font-size: 13px; }
.msg .who { font-weight: bold; color: #7aadff; }
.msg .who.dm { color: #b088ff; }
.msg .who.system { color: #888; font-style: italic; }
.msg .tag { font-size: 11px; color: #555; margin-left: 6px; }
#input-bar { display: flex; padding: 10px; border-top: 1px solid #333; gap: 8px; }
#input-bar input { flex: 1; background: #222; border: 1px solid #444; color: #ccc; padding: 8px; border-radius: 4px; font-family: monospace; }
#input-bar button { background: #3a6aad; color: #fff; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; }
#join-row { display: flex; gap: 4px; }
#join-row input { flex: 1; background: #222; border: 1px solid #333; color: #ccc; padding: 4px 6px; font-size: 12px; border-radius: 3px; }
#join-row button { background: #2a4a2a; color: #8f8; border: none; padding: 4px 8px; font-size: 12px; border-radius: 3px; cursor: pointer; }
</style>
</head>
<body>
<div id="sidebar">
  <div>
    <div id="server-label" style="font-size:11px;color:#555;margin-bottom:8px"></div>
    <h3>Channels</h3>
    <div id="ch-list"></div>
    <div id="join-row" style="margin-top:6px">
      <input id="ch-input" placeholder="#channel">
      <button onclick="joinChannel()">Join</button>
    </div>
  </div>
  <div>
    <h3>Direct Messages</h3>
    <div id="dm-list"></div>
    <div id="join-row" style="margin-top:6px">
      <input id="dm-input" placeholder="username">
      <button onclick="openDM()">DM</button>
    </div>
  </div>
</div>
<div id="main">
  <div id="top-bar">Select a channel or DM to start chatting</div>
  <div id="messages"></div>
  <div id="input-bar">
    <input id="msg-input" placeholder="Message..." onkeydown="if(event.key==='Enter')send()">
    <button onclick="send()">Send</button>
  </div>
</div>

<script>
const name = prompt("Your username:") || "anon";
const ws = new WebSocket("ws://" + location.host + "/ws?name=" + encodeURIComponent(name));

let currentView = null;   // e.g. "room:general" or "dm:alice:bob"
const joined = {};        // redisChannel -> true
const msgStore = {};      // redisChannel -> [messages]

fetch("/status").then(r=>r.json()).then(s => {
  document.getElementById("server-label").textContent = "on " + s.server_id;
});

ws.onmessage = e => {
  const m = JSON.parse(e.data);
  const ch = m.channel || "__system__";
  if (!msgStore[ch]) msgStore[ch] = [];
  msgStore[ch].push(m);
  if (ch === currentView || ch === "__system__") renderMessages();
};

function renderMessages() {
  const el = document.getElementById("messages");
  const ch = currentView;
  const msgs = (ch ? msgStore[ch] : msgStore["__system__"]) || [];
  el.innerHTML = msgs.map(m => {
    const tag = m.server_id ? '<span class="tag">[' + m.server_id + ']</span>' : '';
    if (m.type === "system") {
      return '<div class="msg"><span class="who system">•</span> ' + esc(m.text) + tag + '</div>';
    }
    const cls = m.type === "dm" ? "dm" : "";
    return '<div class="msg"><span class="who ' + cls + '">' + esc(m.from) + '</span>: ' + esc(m.text) + tag + '</div>';
  }).join("");
  el.scrollTop = el.scrollHeight;
}

function switchView(redisChannel, label) {
  currentView = redisChannel;
  document.getElementById("top-bar").textContent = label;
  document.querySelectorAll(".ch-item").forEach(el => el.classList.remove("active"));
  const el = document.getElementById("item-" + redisChannel.replace(/:/g,"_"));
  if (el) el.classList.add("active");
  if (!msgStore[redisChannel]) msgStore[redisChannel] = [];
  renderMessages();
}

function joinChannel() {
  const ch = document.getElementById("ch-input").value.trim().replace(/^#/,"");
  if (!ch) return;
  document.getElementById("ch-input").value = "";
  const redisChannel = "room:" + ch;
  if (joined[redisChannel]) { switchView(redisChannel, "#" + ch); return; }
  ws.send(JSON.stringify({action:"join", channel:ch}));
  joined[redisChannel] = true;
  addSidebarItem("ch-list", redisChannel, "#" + ch);
  switchView(redisChannel, "#" + ch);
}

function openDM() {
  const to = document.getElementById("dm-input").value.trim();
  if (!to || to === name) return;
  document.getElementById("dm-input").value = "";
  const pair = [name, to].sort();
  const redisChannel = "dm:" + pair[0] + ":" + pair[1];
  if (joined[redisChannel]) { switchView(redisChannel, "DM: " + to); return; }
  ws.send(JSON.stringify({action:"join_dm", to:to}));
  joined[redisChannel] = true;
  addSidebarItem("dm-list", redisChannel, to);
  switchView(redisChannel, "DM: " + to);
}

function addSidebarItem(listId, redisChannel, label) {
  const id = "item-" + redisChannel.replace(/:/g,"_");
  if (document.getElementById(id)) return;
  const el = document.createElement("div");
  el.className = "ch-item";
  el.id = id;
  el.textContent = label;
  el.onclick = () => switchView(redisChannel, label);
  document.getElementById(listId).appendChild(el);
}

function send() {
  const input = document.getElementById("msg-input");
  const text = input.value.trim();
  if (!text || !currentView) return;
  input.value = "";
  if (currentView.startsWith("room:")) {
    const ch = currentView.replace("room:","");
    ws.send(JSON.stringify({action:"msg", channel:ch, text}));
  } else if (currentView.startsWith("dm:")) {
    const parts = currentView.split(":");  // dm:alice:bob
    const to = parts[1] === name ? parts[2] : parts[1];
    ws.send(JSON.stringify({action:"dm", to, text}));
  }
}

function esc(s) {
  return String(s).replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");
}
</script>
</body>
</html>`
