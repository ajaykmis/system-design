// Package election implements leader election using Redis SETNX + TTL.
//
// Pattern: Each node tries to acquire a lock key with SETNX. If it succeeds,
// it becomes the leader and must renew the lease periodically. If the leader
// dies, the TTL expires and another node can take over.
//
// This is the same pattern used in distributed systems where a single node
// must coordinate work (e.g., HNSW index rebuilds in the Retrieval service).
package election

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Elector manages leader election for a single election key.
type Elector struct {
	client   *redis.Client
	key      string
	nodeID   string
	leaseTTL time.Duration

	mu       sync.RWMutex
	isLeader bool
	term     int64 // increments each time this node becomes leader
}

// New creates a new Elector.
//
//   - key: the Redis key used for the election (e.g., "leader:index-builder")
//   - nodeID: unique identifier for this node
//   - leaseTTL: how long the lease lasts before expiring
func New(client *redis.Client, key, nodeID string, leaseTTL time.Duration) *Elector {
	return &Elector{
		client:   client,
		key:      key,
		nodeID:   nodeID,
		leaseTTL: leaseTTL,
	}
}

// TryAcquire attempts to become the leader using SETNX.
// Returns true if this node is now the leader.
func (e *Elector) TryAcquire(ctx context.Context) (bool, error) {
	// SETNX: set key only if it doesn't exist, with TTL
	ok, err := e.client.SetNX(ctx, e.key, e.nodeID, e.leaseTTL).Result()
	if err != nil {
		return false, fmt.Errorf("setnx: %w", err)
	}

	if ok {
		e.mu.Lock()
		e.isLeader = true
		e.term++
		e.mu.Unlock()
		log.Printf("[election] Node %s became leader (term %d)", e.nodeID, e.term)
	}

	return ok, nil
}

// RenewLease extends the leader's lease if we are still the leader.
// Returns false if we lost leadership (another node took over).
func (e *Elector) RenewLease(ctx context.Context) (bool, error) {
	// Verify we still hold the lock
	val, err := e.client.Get(ctx, e.key).Result()
	if err == redis.Nil {
		e.mu.Lock()
		e.isLeader = false
		e.mu.Unlock()
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get: %w", err)
	}

	if val != e.nodeID {
		e.mu.Lock()
		e.isLeader = false
		e.mu.Unlock()
		return false, nil
	}

	// Extend TTL
	ok, err := e.client.Expire(ctx, e.key, e.leaseTTL).Result()
	if err != nil {
		return false, fmt.Errorf("expire: %w", err)
	}

	return ok, nil
}

// Resign voluntarily gives up leadership.
func (e *Elector) Resign(ctx context.Context) error {
	e.mu.Lock()
	e.isLeader = false
	e.mu.Unlock()

	// Only delete if we still hold the lock (avoid deleting another leader's key)
	val, err := e.client.Get(ctx, e.key).Result()
	if err != nil {
		return nil // key doesn't exist, nothing to do
	}
	if val == e.nodeID {
		e.client.Del(ctx, e.key)
		log.Printf("[election] Node %s resigned leadership", e.nodeID)
	}
	return nil
}

// IsLeader returns whether this node is currently the leader.
func (e *Elector) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isLeader
}

// GetLeader returns the current leader's node ID, or "" if none.
func (e *Elector) GetLeader(ctx context.Context) (string, error) {
	val, err := e.client.Get(ctx, e.key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// Term returns how many times this node has become leader.
func (e *Elector) Term() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.term
}

// RunElectionLoop runs a background loop that continuously tries to
// acquire or renew the leadership lease. Call cancel() to stop.
func (e *Elector) RunElectionLoop(ctx context.Context) {
	// Renew at 1/3 of TTL to allow for some jitter
	renewInterval := e.leaseTTL / 3

	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Resign on shutdown
			e.Resign(context.Background())
			return
		case <-ticker.C:
			if e.IsLeader() {
				renewed, err := e.RenewLease(ctx)
				if err != nil {
					log.Printf("[election] Renew error: %v", err)
					continue
				}
				if !renewed {
					log.Printf("[election] Node %s lost leadership", e.nodeID)
				}
			} else {
				acquired, err := e.TryAcquire(ctx)
				if err != nil {
					log.Printf("[election] Acquire error: %v", err)
					continue
				}
				if acquired {
					log.Printf("[election] Node %s acquired leadership", e.nodeID)
				}
			}
		}
	}
}

// Status returns the current election state for debugging.
func (e *Elector) Status(ctx context.Context) map[string]any {
	leader, _ := e.GetLeader(ctx)
	ttl, _ := e.client.TTL(ctx, e.key).Result()

	return map[string]any{
		"node_id":      e.nodeID,
		"is_leader":    e.IsLeader(),
		"current_leader": leader,
		"term":         e.Term(),
		"lease_ttl":    e.leaseTTL.String(),
		"lease_remaining": ttl.String(),
	}
}
