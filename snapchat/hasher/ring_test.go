package hasher

import (
	"fmt"
	"math"
	"testing"
)

func TestBasicRouting(t *testing.T) {
	r := New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")

	// Same key should always route to the same node
	node := r.Get("content-abc")
	for i := 0; i < 100; i++ {
		if got := r.Get("content-abc"); got != node {
			t.Fatalf("inconsistent routing: expected %s, got %s", node, got)
		}
	}
}

func TestDistribution(t *testing.T) {
	r := New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")

	// Generate 10000 keys and check distribution
	keys := make([]string, 10000)
	for i := range keys {
		keys[i] = fmt.Sprintf("content-%d", i)
	}

	dist := r.Distribution(keys)
	t.Logf("Distribution: %v", dist)

	// Each node should get roughly 3333 keys (±20%)
	expected := float64(len(keys)) / float64(len(dist))
	for node, count := range dist {
		deviation := math.Abs(float64(count)-expected) / expected
		if deviation > 0.20 {
			t.Errorf("node %s has %d keys (%.1f%% deviation from %.0f expected)",
				node, count, deviation*100, expected)
		}
	}
}

func TestMinimalRedistribution(t *testing.T) {
	r := New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")

	// Record initial assignments
	keys := make([]string, 1000)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}

	before := make(map[string]string)
	for _, key := range keys {
		before[key] = r.Get(key)
	}

	// Add a 4th node
	r.Add("node-4")

	// Count how many keys moved
	moved := 0
	for _, key := range keys {
		if r.Get(key) != before[key] {
			moved++
		}
	}

	// Ideally ~K/N = 1000/4 = 250 keys should move (±50%)
	expectedMoved := float64(len(keys)) / 4.0
	t.Logf("Keys moved: %d (expected ~%.0f)", moved, expectedMoved)

	if float64(moved) > expectedMoved*1.5 {
		t.Errorf("too many keys moved: %d (max expected %.0f)", moved, expectedMoved*1.5)
	}
}

func TestRemoveNode(t *testing.T) {
	r := New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")

	// All 3 nodes should get traffic
	dist := r.Distribution([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"})
	activeNodes := 0
	for _, count := range dist {
		if count > 0 {
			activeNodes++
		}
	}

	r.Remove("node-2")
	nodes := r.Nodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// All keys should now route to node-1 or node-3
	for _, key := range []string{"a", "b", "c"} {
		node := r.Get(key)
		if node != "node-1" && node != "node-3" {
			t.Errorf("key %s routed to removed node: %s", key, node)
		}
	}
}
