package election

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: "localhost:6380",
	})
}

func TestAcquireAndRenew(t *testing.T) {
	client := newTestClient()
	ctx := context.Background()

	// Clean up
	client.Del(ctx, "test:leader")
	defer client.Del(ctx, "test:leader")

	e := New(client, "test:leader", "node-1", 5*time.Second)

	// Should acquire
	ok, err := e.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !ok {
		t.Fatal("expected to acquire leadership")
	}
	if !e.IsLeader() {
		t.Fatal("expected IsLeader() = true")
	}
	if e.Term() != 1 {
		t.Fatalf("expected term 1, got %d", e.Term())
	}

	// Should renew
	renewed, err := e.RenewLease(ctx)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed {
		t.Fatal("expected renewal to succeed")
	}

	// Check leader
	leader, err := e.GetLeader(ctx)
	if err != nil {
		t.Fatalf("get leader: %v", err)
	}
	if leader != "node-1" {
		t.Fatalf("expected leader node-1, got %s", leader)
	}
}

func TestTwoNodes(t *testing.T) {
	client := newTestClient()
	ctx := context.Background()

	client.Del(ctx, "test:leader2")
	defer client.Del(ctx, "test:leader2")

	e1 := New(client, "test:leader2", "node-A", 5*time.Second)
	e2 := New(client, "test:leader2", "node-B", 5*time.Second)

	// Node A acquires
	ok, _ := e1.TryAcquire(ctx)
	if !ok {
		t.Fatal("node-A should acquire")
	}

	// Node B should fail
	ok, _ = e2.TryAcquire(ctx)
	if ok {
		t.Fatal("node-B should NOT acquire while A holds the lease")
	}
	if e2.IsLeader() {
		t.Fatal("node-B should not be leader")
	}

	// Node A resigns
	e1.Resign(ctx)
	if e1.IsLeader() {
		t.Fatal("node-A should not be leader after resign")
	}

	// Now Node B should acquire
	ok, _ = e2.TryAcquire(ctx)
	if !ok {
		t.Fatal("node-B should acquire after A resigned")
	}
	if !e2.IsLeader() {
		t.Fatal("node-B should be leader")
	}
}

func TestLeaseExpiry(t *testing.T) {
	client := newTestClient()
	ctx := context.Background()

	client.Del(ctx, "test:leader3")
	defer client.Del(ctx, "test:leader3")

	// Very short TTL
	e1 := New(client, "test:leader3", "node-X", 1*time.Second)
	e2 := New(client, "test:leader3", "node-Y", 5*time.Second)

	ok, _ := e1.TryAcquire(ctx)
	if !ok {
		t.Fatal("node-X should acquire")
	}

	// Wait for lease to expire
	time.Sleep(1500 * time.Millisecond)

	// Node Y should now be able to acquire
	ok, _ = e2.TryAcquire(ctx)
	if !ok {
		t.Fatal("node-Y should acquire after X's lease expired")
	}
}
