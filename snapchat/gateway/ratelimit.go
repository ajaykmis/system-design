package main

import (
	"net/http"
	"sync"
	"time"
)

// TokenBucket implements a per-key token bucket rate limiter.
// In production this would be Redis-backed; here it's in-memory for the MVP.
type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64       // tokens added per second
	burst   int           // max tokens (bucket capacity)
	cleanup time.Duration // how often to prune stale buckets
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

func NewTokenBucket(rate float64, burst int) *TokenBucket {
	tb := &TokenBucket{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
		cleanup: 5 * time.Minute,
	}
	go tb.cleanupLoop()
	return tb
}

// Allow checks if a request for the given key is allowed.
func (tb *TokenBucket) Allow(key string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, exists := tb.buckets[key]
	now := time.Now()

	if !exists {
		tb.buckets[key] = &bucket{
			tokens:   float64(tb.burst) - 1,
			lastFill: now,
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * tb.rate
	if b.tokens > float64(tb.burst) {
		b.tokens = float64(tb.burst)
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

func (tb *TokenBucket) cleanupLoop() {
	ticker := time.NewTicker(tb.cleanup)
	for range ticker.C {
		tb.mu.Lock()
		cutoff := time.Now().Add(-tb.cleanup)
		for key, b := range tb.buckets {
			if b.lastFill.Before(cutoff) {
				delete(tb.buckets, key)
			}
		}
		tb.mu.Unlock()
	}
}

// RateLimitMiddleware wraps an http.Handler with per-IP rate limiting.
func RateLimitMiddleware(limiter *TokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = forwarded
		}

		if !limiter.Allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}
