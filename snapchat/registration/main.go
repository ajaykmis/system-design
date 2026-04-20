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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	store, err := NewStore(dsn)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer store.Close()

	sms := &MockSMS{}
	handler := NewHandler(store, sms)

	mux := http.NewServeMux()
	mux.HandleFunc("/register", handler.Register)
	mux.HandleFunc("/verify", handler.Verify)
	mux.HandleFunc("/resend", handler.Resend)
	mux.HandleFunc("/health", handler.Health)

	log.Printf("Registration service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
