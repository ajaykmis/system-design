package main

import (
	"log"
	"net/http"
	"os"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := getEnv("PORT", "8080")
	registrationURL := getEnv("REGISTRATION_URL", "http://localhost:8081")
	authURL := getEnv("AUTH_URL", "http://localhost:8082")
	ingestionURL := getEnv("INGESTION_URL", "http://localhost:8090")
	rankingURL := getEnv("RANKING_URL", "http://localhost:8092")
	retrievalURL := getEnv("RETRIEVAL_URL", "http://localhost:8091")

	// Routes: strip /api/v1 prefix, forward the rest to the backend
	stripPrefix := "/api/v1"
	routes := []Route{
		{Prefix: "/api/v1/register", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/verify", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/resend", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/token/", Backend: authURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/content", Backend: ingestionURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/events", Backend: ingestionURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/feed", Backend: rankingURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/debug/", Backend: retrievalURL, StripPrefix: stripPrefix},
	}

	// Public paths (no auth required)
	publicPaths := map[string]bool{
		"/api/v1/register": true,
		"/api/v1/verify":   true,
		"/api/v1/resend":   true,
		"/api/v1/token/refresh": true,
		"/health":          true,
	}

	proxy := NewProxy(routes)

	// Health endpoint on the gateway itself
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", proxy)

	// Middleware chain: logging → rate limit → auth → proxy
	limiter := NewTokenBucket(10, 20) // 10 req/s sustained, burst of 20
	handler := LoggingMiddleware(
		RateLimitMiddleware(limiter,
			AuthMiddleware(authURL, publicPaths, mux),
		),
	)

	log.Printf("Gateway listening on :%s", port)
	log.Printf("  Registration → %s", registrationURL)
	log.Printf("  Auth         → %s", authURL)
	log.Printf("  Ingestion    → %s", ingestionURL)
	log.Printf("  Ranking      → %s", rankingURL)
	log.Printf("  Retrieval    → %s", retrievalURL)

	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
