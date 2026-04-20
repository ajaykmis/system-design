package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var phoneRegex = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

type Handler struct {
	store *Store
	sms   SMSProvider
}

func NewHandler(store *Store, sms SMSProvider) *Handler {
	return &Handler{store: store, sms: sms}
}

// --- Request/Response types ---

type RegisterRequest struct {
	Phone    string `json:"phone"`
	DeviceID string `json:"device_id"`
}

type RegisterResponse struct {
	RequestID string `json:"request_id"`
	ExpiresIn int    `json:"expires_in"`
}

type VerifyRequest struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
}

type VerifyResponse struct {
	UserID string `json:"user_id"`
}

type ResendRequest struct {
	RequestID string `json:"request_id"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// --- Handlers ---

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if !phoneRegex.MatchString(req.Phone) {
		writeError(w, http.StatusBadRequest, "invalid phone number format, expected E.164 (e.g. +14155551234)")
		return
	}

	// Rate limit: max 5 codes per phone per hour
	ctx := r.Context()
	count, err := h.store.CountRecentCodes(ctx, req.Phone, time.Now().Add(-1*time.Hour))
	if err != nil {
		log.Printf("ERROR counting recent codes: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count >= 5 {
		writeError(w, http.StatusTooManyRequests, "too many verification attempts, try again later")
		return
	}

	code := generateCode()
	requestID := uuid.New().String()
	expiresAt := time.Now().Add(5 * time.Minute)

	if err := h.store.CreateVerification(ctx, requestID, req.Phone, code, req.DeviceID, expiresAt); err != nil {
		log.Printf("ERROR creating verification: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.sms.Send(req.Phone, code); err != nil {
		log.Printf("ERROR sending SMS: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to send verification code")
		return
	}

	writeJSON(w, http.StatusOK, RegisterResponse{
		RequestID: requestID,
		ExpiresIn: 300,
	})
}

func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()
	v, err := h.store.GetVerification(ctx, req.RequestID)
	if err != nil {
		log.Printf("ERROR getting verification: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "unknown request_id")
		return
	}

	if v.Verified {
		writeError(w, http.StatusGone, "code already used")
		return
	}

	if time.Now().After(v.ExpiresAt) {
		writeError(w, http.StatusGone, "code expired")
		return
	}

	if v.Attempts >= v.MaxAttempts {
		writeError(w, http.StatusTooManyRequests, "too many attempts, request a new code")
		return
	}

	// Increment attempt counter before checking code
	if err := h.store.IncrementAttempts(ctx, req.RequestID); err != nil {
		log.Printf("ERROR incrementing attempts: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if req.Code != v.Code {
		remaining := v.MaxAttempts - v.Attempts - 1
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("wrong code, %d attempts remaining", remaining))
		return
	}

	if err := h.store.MarkVerified(ctx, req.RequestID); err != nil {
		log.Printf("ERROR marking verified: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	userID, err := h.store.CreateUser(ctx, v.Phone)
	if err != nil {
		log.Printf("ERROR creating user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, VerifyResponse{
		UserID: userID,
	})
}

func (h *Handler) Resend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req ResendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()
	v, err := h.store.GetVerification(ctx, req.RequestID)
	if err != nil {
		log.Printf("ERROR getting verification: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "unknown request_id")
		return
	}

	// Rate limit: max 5 codes per phone per hour
	count, err := h.store.CountRecentCodes(ctx, v.Phone, time.Now().Add(-1*time.Hour))
	if err != nil {
		log.Printf("ERROR counting recent codes: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count >= 5 {
		writeError(w, http.StatusTooManyRequests, "too many verification attempts, try again later")
		return
	}

	// Create a new verification with a fresh code
	code := generateCode()
	newRequestID := uuid.New().String()
	expiresAt := time.Now().Add(5 * time.Minute)

	if err := h.store.CreateVerification(ctx, newRequestID, v.Phone, code, "", expiresAt); err != nil {
		log.Printf("ERROR creating verification: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.sms.Send(v.Phone, code); err != nil {
		log.Printf("ERROR sending SMS: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to send verification code")
		return
	}

	writeJSON(w, http.StatusOK, RegisterResponse{
		RequestID: newRequestID,
		ExpiresIn: 300,
	})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Helpers ---

func generateCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
