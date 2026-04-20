# Consistent Hash Ring

Distributes keys across nodes with minimal redistribution when nodes are added or removed. Used by the Retrieval service to assign content to index shards.

## System Design Concepts

- **Consistent hashing** — keys map to positions on a ring. Each node owns the range from its position to the next node. Adding a node only moves K/N keys (vs. K keys with modular hashing)
- **Virtual nodes** (150 per physical node) — each physical node has many positions on the ring, ensuring uniform distribution even with few physical nodes
- **O(log N) lookups** — binary search on the sorted ring for the first position >= key hash

## API (Go library)

```go
import "snapchat/hasher"

ring := hasher.New(150)  // 150 vnodes per node
ring.Add("node-1")
ring.Add("node-2")
ring.Add("node-3")

node := ring.Get("content-abc")  // → "node-2"

ring.Add("node-4")  // only ~25% of keys move
ring.Remove("node-2")

dist := ring.Distribution(keys)  // map[string]int
info := ring.Info()               // {nodes, vnodes_per_node, total_vnodes}
```

## Running Tests

```bash
cd snapchat/hasher
go test -v ./...
```

## Test Results

- **Distribution**: ~33.3% per node with 3 nodes (3257/3393/3350 for 10K keys)
- **Redistribution**: 256 of 1000 keys moved when adding a 4th node (expected ~250 = K/N)
