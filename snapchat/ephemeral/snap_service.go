package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultTTLAfterOpen = 10             // seconds
	defaultMaxViews     = 1              // single view
	maxUnopendTTL       = 30 * 24 * 3600 // 30 days in seconds
)

// SnapService is the core business logic for ephemeral messaging.
// It orchestrates the blob store, key store, and message metadata.
type SnapService struct {
	mu       sync.RWMutex
	messages map[string]*SnapMessage // messageID -> message

	blobs *BlobStore
	keys  *KeyStore

	// Callbacks for real-time notifications (Pub/Sub in production)
	OnSnapReceived func(msg *SnapMessage)
	OnSnapOpened   func(msg *SnapMessage)
	OnSnapExpired  func(msg *SnapMessage)
}

func NewSnapService(blobs *BlobStore, keys *KeyStore) *SnapService {
	return &SnapService{
		messages: make(map[string]*SnapMessage),
		blobs:    blobs,
		keys:     keys,
	}
}

// SendSnap encrypts content, stores the blob, and creates message metadata.
func (s *SnapService) SendSnap(req SendSnapRequest) (*SnapMessage, error) {
	msgID := uuid.New().String()
	keyID := "key-" + msgID
	blobRef := "blob-" + msgID

	// 1. Generate per-message encryption key
	key, err := s.keys.GenerateKey(keyID)
	if err != nil {
		return nil, fmt.Errorf("key generation failed: %w", err)
	}

	// 2. Encrypt the content
	encrypted, err := Encrypt(req.Content, key)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	// 3. Store encrypted blob
	if err := s.blobs.Put(blobRef, encrypted); err != nil {
		return nil, fmt.Errorf("blob storage failed: %w", err)
	}

	// 4. Set defaults
	ttl := req.TTLAfterOpen
	if ttl <= 0 {
		ttl = defaultTTLAfterOpen
	}
	maxViews := req.MaxViews
	if maxViews <= 0 {
		maxViews = defaultMaxViews
	}

	// 5. Create message metadata
	now := time.Now()
	msg := &SnapMessage{
		ID:           msgID,
		FromUserID:   req.FromUserID,
		ToUserID:     req.ToUserID,
		BlobRef:      blobRef,
		KeyID:        keyID,
		State:        StatePending,
		TTLAfterOpen: ttl,
		MaxViews:     maxViews,
		ViewCount:    0,
		CreatedAt:    now,
		ExpiresAt:    now.Add(time.Duration(maxUnopendTTL) * time.Second),
	}

	s.mu.Lock()
	s.messages[msgID] = msg
	s.mu.Unlock()

	log.Printf("[SnapService] snap sent: %s", msg.Summary())

	if s.OnSnapReceived != nil {
		s.OnSnapReceived(msg)
	}

	return msg, nil
}

// OpenSnap is called when the recipient opens a snap.
// Returns the decrypted content and starts the view timer.
func (s *SnapService) OpenSnap(messageID, userID string) ([]byte, *SnapMessage, error) {
	s.mu.Lock()
	msg, ok := s.messages[messageID]
	if !ok {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("message %s not found", messageID)
	}

	// Verify recipient
	if msg.ToUserID != userID {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("user %s is not the recipient", userID)
	}

	// Check if already expired
	if msg.State == StateExpired {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("snap %s has expired", messageID)
	}

	// Check view count
	if msg.ViewCount >= msg.MaxViews {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("snap %s has reached max views (%d)", messageID, msg.MaxViews)
	}

	// Update state
	now := time.Now()
	if msg.State == StatePending || msg.State == StateDelivered {
		msg.State = StateOpened
		msg.OpenedAt = &now
		// Set expiry based on TTL after open
		msg.ExpiresAt = now.Add(time.Duration(msg.TTLAfterOpen) * time.Second)
	}
	msg.ViewCount++
	s.mu.Unlock()

	// Retrieve encryption key
	key, err := s.keys.GetKey(msg.KeyID)
	if err != nil {
		return nil, nil, fmt.Errorf("key retrieval failed: %w", err)
	}

	// Retrieve and decrypt blob
	encrypted, err := s.blobs.Get(msg.BlobRef)
	if err != nil {
		return nil, nil, fmt.Errorf("blob retrieval failed: %w", err)
	}

	plaintext, err := Decrypt(encrypted, key)
	if err != nil {
		return nil, nil, fmt.Errorf("decryption failed: %w", err)
	}

	log.Printf("[SnapService] snap opened: %s (view %d/%d)", msg.Summary(), msg.ViewCount, msg.MaxViews)

	if s.OnSnapOpened != nil {
		s.OnSnapOpened(msg)
	}

	return plaintext, msg, nil
}

// ViewComplete is called when the client's view timer expires.
// Triggers crypto-shredding if max views reached.
func (s *SnapService) ViewComplete(messageID, userID string) error {
	s.mu.Lock()
	msg, ok := s.messages[messageID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("message %s not found", messageID)
	}

	if msg.ToUserID != userID {
		s.mu.Unlock()
		return fmt.Errorf("user %s is not the recipient", userID)
	}

	if msg.ViewCount >= msg.MaxViews {
		// All views consumed — expire and crypto-shred
		msg.State = StateExpired
		now := time.Now()
		msg.ExpiredAt = &now
		s.mu.Unlock()

		s.purgeSnap(msg)
		return nil
	}

	s.mu.Unlock()
	return nil
}

// purgeSnap deletes the blob and destroys the encryption key.
func (s *SnapService) purgeSnap(msg *SnapMessage) {
	log.Printf("[SnapService] purging snap %s (crypto-shredding)", msg.ID[:8])

	// 1. Destroy the encryption key (makes blob unreadable everywhere)
	s.keys.DestroyKey(msg.KeyID)

	// 2. Delete the blob (reclaim storage)
	s.blobs.Delete(msg.BlobRef)

	if s.OnSnapExpired != nil {
		s.OnSnapExpired(msg)
	}
}

// GetPendingSnaps returns all unread snaps for a user.
func (s *SnapService) GetPendingSnaps(userID string) []*SnapMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pending []*SnapMessage
	for _, msg := range s.messages {
		if msg.ToUserID == userID && msg.State != StateExpired {
			pending = append(pending, msg)
		}
	}
	return pending
}

// GetMessage returns a single message by ID.
func (s *SnapService) GetMessage(messageID string) (*SnapMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.messages[messageID]
	return msg, ok
}

// ExpireByTTL is called by the reaper to expire messages past their TTL.
// Returns the number of messages expired.
func (s *SnapService) ExpireByTTL() int {
	now := time.Now()
	var toExpire []*SnapMessage

	s.mu.Lock()
	for _, msg := range s.messages {
		if msg.State != StateExpired && now.After(msg.ExpiresAt) {
			msg.State = StateExpired
			expired := now
			msg.ExpiredAt = &expired
			toExpire = append(toExpire, msg)
		}
	}
	s.mu.Unlock()

	for _, msg := range toExpire {
		s.purgeSnap(msg)
	}

	return len(toExpire)
}
