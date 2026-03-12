# go-distributed-kv

A fault-tolerant, distributed key-value store built from scratch in Go. Uses the **Raft consensus algorithm** to maintain consistency across a cluster of nodes, with support for leader election, log replication, and automatic failover.

## Features

- Raft-based consensus (leader election + log replication)
- Linearizable reads and writes via the Raft log
- Snapshotting and log compaction to bound storage growth
- gRPC transport layer between nodes
- Simple HTTP API for client operations (`GET`, `PUT`, `DELETE`)
- Membership changes: nodes can join/leave a running cluster

## Architecture

```
         Client (HTTP)
              |
         ┌────▼─────┐
         │  HTTP    │
         │  Server  │
         └────┬─────┘
              │
         ┌────▼─────────────────────┐
         │       Raft Node          │
         │  ┌──────────────────┐    │
         │  │  Raft Log        │    │
         │  │  (replicated)    │    │
         │  └──────────────────┘    │
         │  ┌──────────────────┐    │
         │  │  State Machine   │    │
         │  │  (KV Store)      │    │
         │  └──────────────────┘    │
         └──────────┬───────────────┘
                    │ gRPC
           ┌────────┴────────┐
         Node 2           Node 3
```

## Getting Started

### Prerequisites

- Go 1.21+
- `protoc` with the Go gRPC plugin (for regenerating protobufs)

### Running a 3-node cluster locally

```bash
# Clone the repo
git clone https://github.com/Harsh7115/go-distributed-kv
cd go-distributed-kv

# Start node 1 (leader candidate)
go run ./cmd/server --id=1 --http=:8080 --raft=:9090 --peers=localhost:9091,localhost:9092

# Start node 2
go run ./cmd/server --id=2 --http=:8081 --raft=:9091 --peers=localhost:9090,localhost:9092

# Start node 3
go run ./cmd/server --id=3 --http=:8082 --raft=:9092 --peers=localhost:9090,localhost:9091
```

### Client usage

```bash
# Write a key
curl -X PUT http://localhost:8080/kv/foo -d '{"value": "bar"}'

# Read a key
curl http://localhost:8080/kv/foo
# {"key":"foo","value":"bar"}

# Delete a key
curl -X DELETE http://localhost:8080/kv/foo
```

## Project Structure

```
go-distributed-kv/
├── cmd/
│   └── server/         # Entry point
├── internal/
│   ├── raft/           # Raft implementation
│   │   ├── node.go     # Core Raft state machine
│   │   ├── log.go      # Persistent log entries
│   │   ├── snapshot.go # Log compaction
│   │   └── transport.go# gRPC peer transport
│   ├── store/          # KV store state machine
│   │   └── store.go
│   └── server/         # HTTP API handlers
│       └── server.go
├── proto/
│   └── raft.proto      # gRPC service definition
├── go.mod
└── README.md
```

## Implementation Notes

The Raft implementation follows the original paper by Ongaro & Ousterhout (2014). Key design choices:

- **Log entries** are stored in a write-ahead log (WAL) backed by `bbolt` for durability
- **Snapshots** are triggered when the log exceeds a configurable threshold
- **Leader leases** are used to serve reads without a full Raft round-trip (when clock skew is bounded)
- **gRPC streaming** is used for AppendEntries to reduce connection overhead on high-write workloads

## Benchmarks

On a 3-node cluster (local loopback, M1 MacBook):

| Operation | Throughput | p99 Latency |
|-----------|-----------|-------------|
| PUT       | ~12k ops/s | 4.2ms |
| GET (linearizable) | ~18k ops/s | 2.8ms |
| GET (follower, stale) | ~45k ops/s | 1.1ms |

## References

- [In Search of an Understandable Consensus Algorithm (Raft paper)](https://raft.github.io/raft.pdf)
- [etcd/raft](https://github.com/etcd-io/etcd/tree/main/raft) — production Raft implementation for reference
- [MIT 6.5840 Distributed Systems](https://pdos.csail.mit.edu/6.824/)

## License

MIT

<!-- Last reviewed: March 2026 -->
