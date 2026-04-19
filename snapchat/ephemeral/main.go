package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	// Initialize stores
	blobs := NewBlobStore()
	keys := NewKeyStore()
	service := NewSnapService(blobs, keys)

	// Wire up notification callbacks (Pub/Sub in production)
	service.OnSnapReceived = func(msg *SnapMessage) {
		log.Printf("[Notify] %s has a new snap from %s", msg.ToUserID, msg.FromUserID)
	}
	service.OnSnapOpened = func(msg *SnapMessage) {
		log.Printf("[Notify] %s opened snap from %s", msg.ToUserID, msg.FromUserID)
	}
	service.OnSnapExpired = func(msg *SnapMessage) {
		log.Printf("[Notify] snap %s expired and was crypto-shredded", msg.ID[:8])
	}

	// Start TTL reaper (checks every 5 seconds for this demo)
	reaper := NewReaper(service, 5*time.Second)
	reaper.Start()
	defer reaper.Stop()

	// --- HTTP API ---

	// POST /snap/send — send a snap
	http.HandleFunc("/snap/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req SendSnapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.FromUserID == "" || req.ToUserID == "" {
			http.Error(w, "from_user_id and to_user_id required", http.StatusBadRequest)
			return
		}
		if len(req.Content) == 0 {
			// Allow sending text as content for the demo
			http.Error(w, "content required", http.StatusBadRequest)
			return
		}

		msg, err := service.SendSnap(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msg)
	})

	// POST /snap/open — open/view a snap (returns decrypted content)
	http.HandleFunc("/snap/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var ev ViewEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		content, msg, err := service.OpenSnap(ev.MessageID, ev.UserID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":  msg,
			"content":  string(content),
			"warning":  fmt.Sprintf("This snap expires in %d seconds", msg.TTLAfterOpen),
		})
	})

	// POST /snap/viewed — client reports view timer expired
	http.HandleFunc("/snap/viewed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var ev ViewEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := service.ViewComplete(ev.MessageID, ev.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GET /snap/pending?user_id=xxx — list unread snaps for a user
	http.HandleFunc("/snap/pending", func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			http.Error(w, "user_id required", http.StatusBadRequest)
			return
		}

		msgs := service.GetPendingSnaps(userID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	})

	// GET /snap/status?id=xxx — check a snap's state
	http.HandleFunc("/snap/status", func(w http.ResponseWriter, r *http.Request) {
		msgID := r.URL.Query().Get("id")
		if msgID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}

		msg, ok := service.GetMessage(msgID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Also report storage state
		blobExists := blobs.Exists(msg.BlobRef)
		keyDestroyed := keys.IsDestroyed(msg.KeyID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":       msg,
			"blob_exists":   blobExists,
			"key_destroyed": keyDestroyed,
		})
	})

	// GET /stats — system overview
	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		blobTotal, blobActive := blobs.Stats()
		keyTotal, keyActive, keyDestroyed := keys.Stats()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"blobs": map[string]int{"total": blobTotal, "active": blobActive},
			"keys":  map[string]int{"total": keyTotal, "active": keyActive, "destroyed": keyDestroyed},
		})
	})

	// GET / — demo page with curl examples
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, demoText)
	})

	addr := ":8085"
	log.Printf("Ephemeral messaging server running on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

const demoText = `Snapchat Ephemeral Messaging MVP
================================

Try this flow with curl:

1. SEND a snap (Alice → Bob):
   curl -s -X POST http://localhost:8085/snap/send \
     -H 'Content-Type: application/json' \
     -d '{"from_user_id":"alice","to_user_id":"bob","content":"aGVsbG8gYm9i","ttl_after_open":10,"max_views":1}'

   (content is base64 of "hello bob")
   → Note the "id" in the response

2. CHECK pending snaps for Bob:
   curl -s http://localhost:8085/snap/pending?user_id=bob | jq

3. OPEN the snap (as Bob):
   curl -s -X POST http://localhost:8085/snap/open \
     -H 'Content-Type: application/json' \
     -d '{"message_id":"<ID_FROM_STEP_1>","user_id":"bob"}'

   → Returns decrypted content. Timer starts.

4. REPORT view complete (Bob's client timer expired):
   curl -s -X POST http://localhost:8085/snap/viewed \
     -H 'Content-Type: application/json' \
     -d '{"message_id":"<ID_FROM_STEP_1>","user_id":"bob"}'

   → Triggers crypto-shredding: key destroyed, blob deleted.

5. VERIFY the snap is gone:
   curl -s http://localhost:8085/snap/status?id=<ID_FROM_STEP_1> | jq

   → blob_exists=false, key_destroyed=true

6. TRY to open again (should fail):
   curl -s -X POST http://localhost:8085/snap/open \
     -H 'Content-Type: application/json' \
     -d '{"message_id":"<ID_FROM_STEP_1>","user_id":"bob"}'

   → "snap has expired" or "reached max views"

7. CHECK system stats:
   curl -s http://localhost:8085/stats | jq

Endpoints:
  POST /snap/send     — Send a snap
  POST /snap/open     — Open/view a snap
  POST /snap/viewed   — Report view timer expired
  GET  /snap/pending  — List unread snaps
  GET  /snap/status   — Check snap state + storage
  GET  /stats         — System overview
`
