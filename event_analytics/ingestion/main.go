package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/google/uuid"
)

var producer *kafka.Producer
var topic = "raw-events"

type Event struct {
	EventName  string         `json:"event_name"`
	Timestamp  string         `json:"timestamp"`
	UserID     string         `json:"user_id"`
	DeviceID   string         `json:"device_id"`
	Properties map[string]any `json:"properties"`
}

type IngestRequest struct {
	Events []Event `json:"events"`
}

type IngestResponse struct {
	Accepted  int    `json:"accepted"`
	RequestID string `json:"request_id"`
}

func ingestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if len(req.Events) == 0 {
		http.Error(w, `{"error":"no events"}`, http.StatusBadRequest)
		return
	}

	accepted := 0
	for _, ev := range req.Events {
		if ev.EventName == "" {
			continue
		}

		// Default timestamp to now
		if ev.Timestamp == "" {
			ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
		}

		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}

		err = producer.Produce(&kafka.Message{
			TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
			Key:            []byte(ev.EventName),
			Value:          data,
		}, nil)
		if err != nil {
			log.Printf("Kafka produce error: %v", err)
			continue
		}
		accepted++
	}

	producer.Flush(100)

	reqID := uuid.New().String()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(IngestResponse{Accepted: accepted, RequestID: reqID})
	log.Printf("Ingested %d/%d events (request %s)", accepted, len(req.Events), reqID[:8])
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func main() {
	kafkaAddr := os.Getenv("KAFKA_BOOTSTRAP")
	if kafkaAddr == "" {
		kafkaAddr = "localhost:29092"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8100"
	}

	var err error
	producer, err = kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers": kafkaAddr,
	})
	if err != nil {
		log.Fatalf("Failed to create Kafka producer: %v", err)
	}
	defer producer.Close()

	// Drain delivery reports in background
	go func() {
		for e := range producer.Events() {
			if m, ok := e.(*kafka.Message); ok && m.TopicPartition.Error != nil {
				log.Printf("Delivery failed: %v", m.TopicPartition.Error)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/events", ingestHandler)
	mux.HandleFunc("/health", healthHandler)

	log.Printf("Ingestion API listening on :%s (Kafka: %s)", port, kafkaAddr)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
