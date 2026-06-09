# go-distributed-kv

A fault-tolerant distributed key-value store built on the **Raft consensus algorithm**, written in Go.

Implements the complete Raft spec: leader election, log replication with majority-quorum commits, and snapshot-based log compaction. Strong consistency (linearizability) — every read reflects all prior writes.

## Architecture

```
          ┌─────────────────────────────┐
          │           Client            │
          └──────────────┬──────────────┘
                         │ RPC
          ┌──────────────▼──────────────┐
          │         Leader Node         │
          │   ┌─────────────────────┐   │
          │   │    Raft Log         │   │
          │   │  [1][2][3]...[N]    │   │
          │   └─────────────────────┘   │
          └───┬──────────────┬──────────┘
AppendEntries │              │ AppendEntries
          ┌───▼────┐    ┌────▼───┐
          │Follower│    │Follower│
          └────────┘    └────────┘
```

### Raft State Machine

```
           ┌──────────────────────────┐
   Start   │                          │ timeout, no leader
    ───►   │         Follower         ├─────────────────────┐
           │                          │                     │
           └──────────────────────────┘                     ▼
                        ▲                           ┌───────────────┐
                        │ discovers leader           │   Candidate   │
                        │ or higher term             │  (requests    │
           ┌────────────┴────────────┐               │    votes)     │
           │                         │               └───────┬───────┘
           │         Leader          │◄──────────────────────┘
           │  (replicates log,        │    receives majority votes
           │   sends heartbeats)      │
           └─────────────────────────┘
```

## Features

- **Leader election** with randomized timeouts to avoid split votes
- **Log replication** with majority-quorum commit guarantee
- **Log compaction** via snapshotting — bounds log size and speeds recovery
- **Linearizable reads** served only by the current leader
- **gRPC transport** for efficient inter-node RPC

## Getting Started

```bash
git clone https://github.com/Harsh7115/go-distributed-kv
cd go-distributed-kv
go build ./...
```

### Start a 3-node cluster

Run each command in a separate terminal:

```bash
go run main.go --id 1 --port 8001 --peers localhost:8002,localhost:8003
go run main.go --id 2 --port 8002 --peers localhost:8001,localhost:8003
go run main.go --id 3 --port 8003 --peers localhost:8001,localhost:8002
```

### Client operations

```bash
# Write
go run client/main.go --op put --key foo --value bar

# Read (always hits the current leader)
go run client/main.go --op get --key foo

# Delete
go run client/main.go --op delete --key foo
```

### Fault tolerance demo

```bash
# Kill the leader — a new one is elected within ~500ms
kill $(pgrep -f "id 1")

# Reads and writes continue with no data loss
go run client/main.go --op get --key foo
```

## Tests

```bash
go test ./...                       # unit tests
go test ./raft/... -run TestElection  # election correctness
go test ./raft/... -run TestReplication  # log replication
go test ./raft/... -run TestSnapshot    # log compaction
```

## Configuration Reference

| Flag | Default | Description |
|---|---|---|
| `--id` | required | Unique node ID (integer) |
| `--port` | `8001` | gRPC listen port |
| `--peers` | required | Comma-separated peer addresses |
| `--heartbeat` | `150ms` | Interval between leader heartbeats |
| `--election-min` | `300ms` | Minimum election timeout |
| `--election-max` | `500ms` | Maximum election timeout |
| `--snapshot-threshold` | `1000` | Log entries before triggering snapshot |

The 2:1 election-to-heartbeat ratio ensures followers detect a dead leader within one timeout window.

## Performance

Benchmarked on a 3-node cluster, LAN, 1 KB values:

| Operation | Throughput | p50 Latency | p99 Latency |
|---|---|---|---|
| Write (via leader) | ~12k ops/s | 3ms | 8ms |
| Read (linearizable) | ~18k ops/s | 1ms | 4ms |
| Leader election | — | — | <500ms |

## Tech Stack

Go · Raft · gRPC · Protocol Buffers

---

Built as a deep-dive into distributed systems fundamentals — consensus, fault tolerance, and linearizability.
