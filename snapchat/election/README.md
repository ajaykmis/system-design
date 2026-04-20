# Leader Election

Redis-based leader election using SETNX + TTL. Used by the Retrieval service to coordinate HNSW index builds — only the leader consumes from Kafka and updates the index.

## System Design Concepts

- **SETNX + TTL** — atomic set-if-not-exists with expiry. If the leader dies, the TTL ensures another node can take over (no manual intervention)
- **Lease renewal** — leader must periodically renew at 1/3 of TTL to account for clock skew and network delays
- **Graceful resignation** — on shutdown, the leader deletes its key so failover is instant instead of waiting for TTL
- **Fencing via terms** — each leadership acquisition increments a term counter to detect stale leaders

## API (Go library)

```go
import "snapchat/election"

client := redis.NewClient(&redis.Options{Addr: "localhost:6380"})
e := election.New(client, "leader:index-builder", "node-1", 30*time.Second)

// Manual usage
ok, _ := e.TryAcquire(ctx)
e.RenewLease(ctx)
e.IsLeader()
e.Resign(ctx)

// Background loop (preferred)
ctx, cancel := context.WithCancel(context.Background())
go e.RunElectionLoop(ctx)
// ... later ...
cancel() // triggers resign
```

## Running Tests

```bash
# Requires Redis on port 6380
cd snapchat/election
go test -v ./...
```

## Failure Scenarios

| Scenario | Behavior |
|----------|---------|
| Leader crashes | TTL expires (30s), another node acquires |
| Leader resigns gracefully | Key deleted immediately, instant failover |
| Network partition | Leader can't renew → TTL expires → new leader elected |
| Split brain | Prevented by SETNX atomicity — only one node holds the key |
