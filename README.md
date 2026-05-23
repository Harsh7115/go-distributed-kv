# go-distributed-kv

A fault-tolerant, distributed key-value store built on the **Raft consensus algorithm**, written in Go.

## Features

- **Raft Consensus** — leader election, log replication, and safety guarantees across cluster nodes
- **Log Compaction** — snapshotting to bound log size and speed up recovery
- **Linearizable Reads & Writes** — strong consistency via leader-only serving
- **Fault Tolerance** — survives minority node failures without data loss
- **gRPC Transport** — efficient binary protocol for inter-node communication

## Architecture

```
Client → Leader Node
            ├── AppendEntries RPC → Follower 1
            ├── AppendEntries RPC → Follower 2
            └── Commit on quorum ack
```

The system implements the full Raft specification: leader election with randomized timeouts, log replication with majority quorum commits, and snapshotting for log compaction.

## Getting Started

```bash
git clone https://github.com/Harsh7115/go-distributed-kv
cd go-distributed-kv
go build ./...

# Start a 3-node cluster
go run main.go --id 1 --peers localhost:8002,localhost:8003
go run main.go --id 2 --peers localhost:8001,localhost:8003
go run main.go --id 3 --peers localhost:8001,localhost:8002
```

## Key Design Decisions

- **Heartbeat interval**: 150ms — balances election timeout sensitivity vs. network overhead
- **Election timeout**: 300–500ms randomized — prevents split votes
- **Snapshot threshold**: configurable log index gap before triggering compaction

## Tech Stack

Go · Raft · gRPC · Protocol Buffers

---

Built as a deep-dive into distributed systems fundamentals — consensus, fault tolerance, and linearizability.
