package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type Handler struct {
	store *Store
	jwt   *JWTManager
}

func NewHandler(store *Store, jwt *JWTManager) *Handler {
	return &Handler{store: store, jwt: jwt}
}

// --- Request/Response types ---

type IssueRequest struct {
	UserID string `json:"user_id"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type ValidateResponse struct {
	Valid  bool   `json:"valid"`
	UserID string `json:"user_id,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// --- Handlers ---

// Issue creates a new access + refresh token pair for a user.
// Called internally by the Registration service after phone verification.
func (h *Handler) Issue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req IssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id required")
		return
	}

	ctx := r.Context()
	exists, err := h.store.UserExists(ctx, req.UserID)
	if err != nil {
		log.Printf("ERROR checking user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	accessToken, err := h.jwt.GenerateAccessToken(req.UserID)
	if err != nil {
		log.Printf("ERROR generating access token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	refreshToken, err := h.jwt.GenerateRefreshToken(req.UserID)
	if err != nil {
		log.Printf("ERROR generating refresh token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.store.SaveRefreshToken(ctx, req.UserID, refreshToken, time.Now().Add(7*24*time.Hour)); err != nil {
		log.Printf("ERROR saving refresh token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900, // 15 minutes
	})
}

// Refresh exchanges a valid refresh token for a new token pair.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Parse the refresh token to get user_id
	claims, err := h.jwt.ValidateToken(req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	ctx := r.Context()
	valid, err := h.store.ValidateRefreshToken(ctx, claims.UserID, req.RefreshToken)
	if err != nil {
		log.Printf("ERROR validating refresh token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, "refresh token revoked or expired")
		return
	}

	// Rotate: revoke old, issue new
	if err := h.store.RevokeRefreshToken(ctx, claims.UserID, req.RefreshToken); err != nil {
		log.Printf("ERROR revoking old refresh token: %v", err)
	}

	accessToken, err := h.jwt.GenerateAccessToken(claims.UserID)
	if err != nil {
		log.Printf("ERROR generating access token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	refreshToken, err := h.jwt.GenerateRefreshToken(claims.UserID)
	if err != nil {
		log.Printf("ERROR generating refresh token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.store.SaveRefreshToken(ctx, claims.UserID, refreshToken, time.Now().Add(7*24*time.Hour)); err != nil {
		log.Printf("ERROR saving refresh token: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900,
	})
}

// Validate checks if an access token is valid. Used by the Gateway.
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		writeJSON(w, http.StatusOK, ValidateResponse{Valid: false})
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := h.jwt.ValidateToken(token)
	if err != nil {
		writeJSON(w, http.StatusOK, ValidateResponse{Valid: false})
		return
	}

	writeJSON(w, http.StatusOK, ValidateResponse{Valid: true, UserID: claims.UserID})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
