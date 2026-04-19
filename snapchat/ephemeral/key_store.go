package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"sync"
	"time"
)

// KeyStore manages per-message AES encryption keys.
// Destroying a key makes the corresponding blob unreadable everywhere
// ("crypto-shredding") — the only reliable delete in distributed systems.
type KeyStore struct {
	mu   sync.RWMutex
	keys map[string]*EncryptionKeyRecord // keyID -> record
}

func NewKeyStore() *KeyStore {
	return &KeyStore{keys: make(map[string]*EncryptionKeyRecord)}
}

// GenerateKey creates a new 256-bit AES key and stores it.
func (ks *KeyStore) GenerateKey(keyID string) ([]byte, error) {
	key := make([]byte, 32) // AES-256
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	ks.keys[keyID] = &EncryptionKeyRecord{
		KeyID:     keyID,
		Key:       key,
		CreatedAt: time.Now(),
	}
	log.Printf("[KeyStore] generated key %s", keyID)
	return key, nil
}

// GetKey retrieves a key for decryption.
func (ks *KeyStore) GetKey(keyID string) ([]byte, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	rec, ok := ks.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("key %s not found", keyID)
	}
	if rec.Destroyed {
		return nil, fmt.Errorf("key %s has been destroyed (crypto-shredded)", keyID)
	}
	return rec.Key, nil
}

// DestroyKey permanently zeroes out and marks a key as destroyed.
// After this, the encrypted blob is unrecoverable — this is crypto-shredding.
func (ks *KeyStore) DestroyKey(keyID string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	rec, ok := ks.keys[keyID]
	if !ok {
		return nil // idempotent
	}

	// Zero out the key material in memory
	for i := range rec.Key {
		rec.Key[i] = 0
	}
	rec.Key = nil
	rec.Destroyed = true

	log.Printf("[KeyStore] DESTROYED key %s (crypto-shredded)", keyID)
	return nil
}

// IsDestroyed checks if a key has been crypto-shredded.
func (ks *KeyStore) IsDestroyed(keyID string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	rec, ok := ks.keys[keyID]
	return ok && rec.Destroyed
}

// Stats returns total and active key counts.
func (ks *KeyStore) Stats() (total, active, destroyed int) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	for _, r := range ks.keys {
		total++
		if r.Destroyed {
			destroyed++
		} else {
			active++
		}
	}
	return
}
