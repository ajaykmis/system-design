package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed static/*
var staticFiles embed.FS

var rdb *redis.Client

type TimeseriesPoint struct {
	Minute string  `json:"minute"`
	Count  float64 `json:"count"`
}

type TimeseriesResponse struct {
	Event       string            `json:"event"`
	Granularity string            `json:"granularity"`
	Data        []TimeseriesPoint `json:"data"`
	QueryTimeMs int64             `json:"query_time_ms"`
}

func timeseriesHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := context.Background()

	eventName := r.URL.Query().Get("event")
	if eventName == "" {
		http.Error(w, `{"error":"event parameter required"}`, http.StatusBadRequest)
		return
	}

	// Parse time range (defaults: last 2 hours)
	endTime := time.Now().UTC()
	startTime := endTime.Add(-2 * time.Hour)

	if s := r.URL.Query().Get("start"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			startTime = t.UTC()
		}
	}
	if s := r.URL.Query().Get("end"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			endTime = t.UTC()
		}
	}

	member := r.URL.Query().Get("member")
	if member == "" {
		member = "total"
	}

	// Generate all minute buckets in the range
	var keys []string
	var minutes []string
	t := startTime.Truncate(time.Minute)
	for !t.After(endTime) {
		bucket := t.Format("2006-01-02T15:04")
		keys = append(keys, fmt.Sprintf("counts:%s:%s", eventName, bucket))
		minutes = append(minutes, bucket)
		t = t.Add(time.Minute)
	}

	// Pipeline all reads in a single Redis round-trip
	pipe := rdb.Pipeline()
	cmds := make([]*redis.FloatCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.ZScore(ctx, key, member)
	}
	pipe.Exec(ctx)

	// Collect results
	data := make([]TimeseriesPoint, 0, len(keys))
	for i, cmd := range cmds {
		count := 0.0
		if val, err := cmd.Result(); err == nil {
			count = val
		}
		data = append(data, TimeseriesPoint{
			Minute: minutes[i],
			Count:  count,
		})
	}

	elapsed := time.Since(start).Milliseconds()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(TimeseriesResponse{
		Event:       eventName,
		Granularity: "minute",
		Data:        data,
		QueryTimeMs: elapsed,
	})

	log.Printf("Timeseries query: event=%s range=%d min, %d points, %dms",
		eventName, len(data), len(data), elapsed)
}

func eventsListHandler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Scan Redis for unique event names from counts:* keys
	eventSet := make(map[string]bool)
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, "counts:*", 1000).Result()
		if err != nil {
			break
		}
		for _, key := range keys {
			// Key format: counts:{event_name}:{minute}
			// Extract event_name
			parts := splitKey(key)
			if len(parts) >= 2 {
				eventSet[parts[1]] = true
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	events := make([]string, 0, len(eventSet))
	for e := range eventSet {
		events = append(events, e)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]any{"events": events})
}

func splitKey(key string) []string {
	// counts:install:2026-04-20T10:05
	var parts []string
	start := 0
	colons := 0
	for i, c := range key {
		if c == ':' {
			colons++
			if colons <= 2 {
				parts = append(parts, key[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, key[start:])
	return parts
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8101"
	}

	host, p, _ := parseAddr(redisAddr)
	rdb = redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", host, p),
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/dashboard/timeseries", timeseriesHandler)
	mux.HandleFunc("/v1/dashboard/events", eventsListHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/", http.FileServer(http.FS(staticFiles)))

	log.Printf("Dashboard API listening on :%s (Redis: %s)", port, redisAddr)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func parseAddr(addr string) (string, int, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p, err := strconv.Atoi(addr[i+1:])
			return addr[:i], p, err
		}
	}
	return addr, 6379, nil
}
