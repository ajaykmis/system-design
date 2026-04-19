package main

import (
	"fmt"
	"time"
)

// MessageState tracks the lifecycle of a snap.
type MessageState int

const (
	StatePending   MessageState = iota // Created, not yet delivered
	StateDelivered                     // Recipient's device downloaded the blob
	StateOpened                        // Recipient opened/viewed the snap
	StateExpired                       // Viewed or TTL exceeded — content purged
)

func (s MessageState) String() string {
	switch s {
	case StatePending:
		return "PENDING"
	case StateDelivered:
		return "DELIVERED"
	case StateOpened:
		return "OPENED"
	case StateExpired:
		return "EXPIRED"
	default:
		return "UNKNOWN"
	}
}

// SnapMessage is the metadata for an ephemeral message.
// The actual media is in blob storage; the encryption key is in the key store.
type SnapMessage struct {
	ID           string       `json:"id"`
	FromUserID   string       `json:"from_user_id"`
	ToUserID     string       `json:"to_user_id"`
	BlobRef      string       `json:"blob_ref"`      // pointer to encrypted blob
	KeyID        string       `json:"key_id"`         // pointer to encryption key
	State        MessageState `json:"state"`
	TTLAfterOpen int          `json:"ttl_after_open"` // seconds viewable after opening
	MaxViews     int          `json:"max_views"`
	ViewCount    int          `json:"view_count"`
	CreatedAt    time.Time    `json:"created_at"`
	ExpiresAt    time.Time    `json:"expires_at"` // hard TTL (e.g., 30 days)
	OpenedAt     *time.Time   `json:"opened_at,omitempty"`
	ExpiredAt    *time.Time   `json:"expired_at,omitempty"`
}

func (m *SnapMessage) Summary() string {
	state := m.State.String()
	return fmt.Sprintf("[%s] %s → %s | state=%s views=%d/%d",
		m.ID[:8], m.FromUserID, m.ToUserID, state, m.ViewCount, m.MaxViews)
}

// EncryptionKeyRecord is stored in the key store.
type EncryptionKeyRecord struct {
	KeyID     string    `json:"key_id"`
	Key       []byte    `json:"-"` // the actual AES key — never serialized
	CreatedAt time.Time `json:"created_at"`
	Destroyed bool      `json:"destroyed"`
}

// BlobRecord tracks an encrypted blob in storage.
type BlobRecord struct {
	Ref       string    `json:"ref"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Deleted   bool      `json:"deleted"`
}

// SendSnapRequest is the API input for sending a snap.
type SendSnapRequest struct {
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	Content      []byte `json:"content"`       // raw media bytes
	TTLAfterOpen int    `json:"ttl_after_open"` // seconds
	MaxViews     int    `json:"max_views"`
}

// ViewEvent is sent by the recipient's client.
type ViewEvent struct {
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
}
