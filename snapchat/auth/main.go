package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://snapuser:snappass@localhost:5433/snapchat?sslmode=disable"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "dev-secret-do-not-use-in-prod"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	store, err := NewStore(dsn)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer store.Close()

	jwtMgr := NewJWTManager(jwtSecret)
	handler := NewHandler(store, jwtMgr)

	mux := http.NewServeMux()
	mux.HandleFunc("/issue", handler.Issue)
	mux.HandleFunc("/refresh", handler.Refresh)
	mux.HandleFunc("/validate", handler.Validate)
	mux.HandleFunc("/health", handler.Health)

	log.Printf("Auth service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
