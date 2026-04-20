// Package hasher implements a consistent hash ring with virtual nodes.
//
// Consistent hashing distributes keys across nodes such that adding or
// removing a node only redistributes K/N keys (where K=total keys, N=nodes),
// rather than reshuffling everything like modular hashing.
//
// Virtual nodes (vnodes) improve distribution uniformity. Each physical
// node maps to multiple positions on the ring, reducing hot spots.
package hasher

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
)

// Ring is a consistent hash ring.
type Ring struct {
	mu             sync.RWMutex
	vnodes         int            // virtual nodes per physical node
	hashes         []uint64       // sorted ring positions
	hashToNode     map[uint64]string // ring position → physical node
	nodes          map[string]bool
}

// New creates a consistent hash ring with the given virtual node count.
// Higher vnodes = more uniform distribution but more memory.
// Typical values: 100-200 vnodes per node.
func New(vnodes int) *Ring {
	return &Ring{
		vnodes:     vnodes,
		hashToNode: make(map[uint64]string),
		nodes:      make(map[string]bool),
	}
}

// Add adds a physical node to the ring.
func (r *Ring) Add(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.nodes[node] {
		return // already added
	}
	r.nodes[node] = true

	for i := 0; i < r.vnodes; i++ {
		h := hash(fmt.Sprintf("%s:%d", node, i))
		r.hashes = append(r.hashes, h)
		r.hashToNode[h] = node
	}
	sort.Slice(r.hashes, func(i, j int) bool { return r.hashes[i] < r.hashes[j] })
}

// Remove removes a physical node from the ring.
func (r *Ring) Remove(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.nodes[node] {
		return
	}
	delete(r.nodes, node)

	// Remove all vnodes for this node
	newHashes := make([]uint64, 0, len(r.hashes)-r.vnodes)
	for _, h := range r.hashes {
		if r.hashToNode[h] != node {
			newHashes = append(newHashes, h)
		} else {
			delete(r.hashToNode, h)
		}
	}
	r.hashes = newHashes
}

// Get returns the node responsible for the given key.
func (r *Ring) Get(key string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.hashes) == 0 {
		return ""
	}

	h := hash(key)
	// Binary search for the first ring position >= h
	idx := sort.Search(len(r.hashes), func(i int) bool {
		return r.hashes[i] >= h
	})
	// Wrap around
	if idx >= len(r.hashes) {
		idx = 0
	}
	return r.hashToNode[r.hashes[idx]]
}

// Distribution returns how many of the given keys map to each node.
func (r *Ring) Distribution(keys []string) map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	dist := make(map[string]int)
	for node := range r.nodes {
		dist[node] = 0
	}
	for _, key := range keys {
		node := r.Get(key)
		dist[node]++
	}
	return dist
}

// Nodes returns the list of physical nodes.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.nodes))
	for node := range r.nodes {
		result = append(result, node)
	}
	sort.Strings(result)
	return result
}

// Info returns ring metadata.
func (r *Ring) Info() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return map[string]any{
		"nodes":           len(r.nodes),
		"vnodes_per_node": r.vnodes,
		"total_vnodes":    len(r.hashes),
	}
}

func hash(key string) uint64 {
	h := md5.Sum([]byte(key))
	return binary.LittleEndian.Uint64(h[:8])
}
