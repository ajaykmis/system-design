package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Hub manages all connected clients and broadcasts messages.
type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]string // conn -> username
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*websocket.Conn]string)}
}

func (h *Hub) Register(conn *websocket.Conn, username string) {
	h.mu.Lock()
	h.clients[conn] = username
	h.mu.Unlock()
	h.Broadcast(fmt.Sprintf("[%s joined the chat]", username), nil)
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	username := h.clients[conn]
	delete(h.clients, conn)
	h.mu.Unlock()
	h.Broadcast(fmt.Sprintf("[%s left the chat]", username), nil)
}

// Broadcast sends a message to all clients except the sender.
func (h *Hub) Broadcast(msg string, sender *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		if conn == sender {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			log.Printf("write error: %v", err)
			conn.Close()
			delete(h.clients, conn)
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins for POC
}

func main() {
	hub := NewHub()

	// Serve a minimal HTML chat client at /
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, htmlPage)
	})

	// WebSocket endpoint
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		username := r.URL.Query().Get("name")
		if username == "" {
			username = "anonymous"
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}
		defer conn.Close()

		hub.Register(conn, username)
		defer hub.Unregister(conn)

		log.Printf("client connected: %s", username)

		// Read loop: receive messages from this client and broadcast
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("read error (%s): %v", username, err)
				break
			}
			formatted := fmt.Sprintf("%s: %s", username, string(msg))
			log.Println(formatted)
			hub.Broadcast(formatted, conn)
		}
	})

	addr := ":8080"
	log.Printf("WebSocket server running on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

const htmlPage = `<!DOCTYPE html>
<html>
<head>
  <title>WebSocket Chat</title>
  <style>
    body { font-family: monospace; max-width: 600px; margin: 40px auto; }
    #log { height: 300px; overflow-y: scroll; border: 1px solid #ccc; padding: 8px; background: #111; color: #0f0; }
    input { width: 70%; padding: 6px; } button { padding: 6px 16px; }
  </style>
</head>
<body>
  <h2>WebSocket Chat Room</h2>
  <div id="log"></div>
  <br>
  <input id="msg" placeholder="Type a message..." onkeydown="if(event.key==='Enter')send()">
  <button onclick="send()">Send</button>

  <script>
    const name = prompt("Enter your name:") || "anonymous";
    const ws = new WebSocket("ws://" + location.host + "/ws?name=" + encodeURIComponent(name));
    const log = document.getElementById("log");

    ws.onopen    = () => appendLog("Connected as " + name);
    ws.onmessage = (e) => appendLog(e.data);
    ws.onclose   = () => appendLog("Disconnected");

    function appendLog(msg) {
      log.innerHTML += msg + "\n";
      log.scrollTop = log.scrollHeight;
    }
    function send() {
      const input = document.getElementById("msg");
      if (input.value) { ws.send(input.value); appendLog("you: " + input.value); input.value = ""; }
    }
  </script>
</body>
</html>`
