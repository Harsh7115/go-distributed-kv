# Architecture: go-distributed-kv

A distributed key-value store built in Go, designed for high availability and strong consistency using the Raft consensus algorithm.

---

## 1. Overview

`go-distributed-kv` is a replicated, linearizable key-value store. Clients issue `GET`, `PUT`, and `DELETE` operations that are transparently replicated across a cluster of nodes. The system tolerates up to ⌊(N-1)/2⌋ node failures in an N-node cluster.

**Design goals:**
- **Strong consistency** — all reads reflect the latest committed write
- **High availability** — majority quorum required, not all nodes
- **Horizontal scalability** — add nodes to increase fault tolerance
- **Simple API** — HTTP/gRPC interface for easy client integration

---

## 2. Component Diagram

```
                 ┌──────────┐
                 │  Client  │
                 └────┬─────┘
                      │ HTTP / gRPC
          ┌───────────▼────────────┐
          │        API Layer       │
          │  (router, middleware)  │
          └───────────┬────────────┘
                      │
          ┌───────────▼────────────┐
          │      KV Service        │  ← request deduplication
          │  (read/write handler)  │     leader forwarding
          └───────────┬────────────┘
                      │
          ┌───────────▼────────────┐
          │     Raft Consensus     │  ← log replication
          │  (leader election,     │     election timeout
          │   log append, commit)  │     heartbeat
          └───────────┬────────────┘
                      │
          ┌───────────▼────────────┐
          │     Storage Engine     │  ← in-memory map + WAL
          │  (apply committed ops) │     snapshot support
          └────────────────────────┘
```

---

## 3. Raft Consensus Layer

The Raft layer implements leader election and log replication following the Raft paper (Ongaro & Ousterhout, 2014).

### 3.1 Node Roles

| Role      | Behaviour |
|-----------|-----------|
| Leader    | Accepts client writes, replicates log entries, sends heartbeats |
| Follower  | Responds to AppendEntries and RequestVote RPCs |
| Candidate | Requests votes during election; transitions to leader on majority |

### 3.2 Log Replication

1. Client sends `PUT k v` to the leader's KV service.
2. Leader appends the command as a new log entry (term + index + command).
3. Leader sends `AppendEntries` RPCs to all followers in parallel.
4. Once a majority acknowledges, the leader advances `commitIndex`.
5. Leader applies the entry to the state machine (storage engine) and responds to the client.
6. Followers apply committed entries on the next `AppendEntries` heartbeat.

### 3.3 Election

- Each node has a randomized election timeout (150–300 ms).
- If a follower receives no heartbeat before timeout, it increments its term and starts an election.
- A candidate wins if it receives `> N/2` votes (including itself).
- Votes are granted only if the candidate's log is at least as up-to-date as the voter's.

### 3.4 Persistence

The following state is persisted to disk (WAL) before responding to any RPC:
- `currentTerm` — latest term seen
- `votedFor` — candidate voted for in current term
- `log[]` — all log entries

---

## 4. Storage Engine

The storage engine is the state machine that Raft drives. It stores the committed key-value data.

### 4.1 In-Memory Map

```go
type KVStore struct {
    mu   sync.RWMutex
    data map[string]string
    // last applied index for deduplication
    lastApplied int
}
```

- `GET` acquires a read lock and returns the value (or 404).
- `PUT` / `DELETE` are applied only after Raft commits the log entry.
- A `sync.RWMutex` allows concurrent reads while writes are exclusive.

### 4.2 Snapshots

To prevent the Raft log from growing unbounded:
1. When the log exceeds a configurable threshold (default: 1000 entries), the node serialises the in-memory map to a snapshot file.
2. The snapshot records the `lastIncludedIndex` and `lastIncludedTerm`.
3. Log entries up to `lastIncludedIndex` are discarded.
4. New nodes joining the cluster can receive the snapshot via `InstallSnapshot` RPC instead of replaying the full log.

---

## 5. KV Service Layer

Sits between the API and Raft. Responsibilities:

### 5.1 Client Request Deduplication

Each client assigns a monotonically increasing `requestId` to every RPC. The KV service tracks the latest completed `requestId` per `clientId`. Duplicate retries (same clientId + requestId) return the cached result without re-applying the command.

### 5.2 Leader Forwarding

Non-leader nodes cannot accept writes. When a follower receives a write request it:
1. Checks its `leaderHint` (set by the last `AppendEntries` message).
2. Returns a redirect response containing the leader's address.
3. The client retries against the leader.

### 5.3 Linearizable Reads

To serve strongly consistent reads without a round-trip through Raft:
1. The leader confirms it is still the leader by sending a heartbeat and waiting for majority acknowledgement (**ReadIndex** approach).
2. Once confirmed, it waits for the apply index to reach at least the read index, then serves the read from its local state machine.

---

## 6. API Layer

### 6.1 HTTP Endpoints

| Method | Path            | Description              |
|--------|-----------------|--------------------------|
| GET    | /kv/{key}       | Read value for key        |
| PUT    | /kv/{key}       | Write value for key       |
| DELETE | /kv/{key}       | Delete key                |
| GET    | /status         | Node role, term, leader   |
| GET    | /metrics        | Prometheus-compatible     |

### 6.2 Request / Response

```json
// PUT /kv/username
{ "value": "harshit" }

// Response 200
{ "ok": true }

// GET /kv/username
// Response 200
{ "key": "username", "value": "harshit" }

// GET /kv/missing
// Response 404
{ "error": "key not found" }
```

---

## 7. Configuration

| Parameter              | Default  | Description                          |
|------------------------|----------|--------------------------------------|
| `cluster.nodes`        | —        | Comma-separated list of peer addresses |
| `raft.electionTimeout` | 150–300ms| Randomised election timeout range    |
| `raft.heartbeatInterval`| 50ms    | Heartbeat period from leader         |
| `storage.snapshotThreshold` | 1000 | Raft log entries before snapshot  |
| `server.port`          | 8080     | HTTP listen port                     |

---

## 8. Fault Tolerance

| Failure Scenario            | Behaviour |
|-----------------------------|-----------|
| Leader crash                | Followers time out and elect a new leader within ~500 ms |
| Follower crash              | Cluster continues with remaining majority; crashed node catches up on restart |
| Network partition           | Minority partition cannot commit new entries; majority partition continues normally |
| Split-brain                 | Prevented by term numbers; stale leader steps down when it sees a higher term |
| Duplicate client requests   | Deduplicated by (clientId, requestId); exactly-once semantics |

---

## 9. Directory Structure

```
go-distributed-kv/
├── cmd/
│   └── server/         # main entry point
├── internal/
│   ├── raft/           # Raft consensus implementation
│   │   ├── raft.go     # core state machine
│   │   ├── rpc.go      # AppendEntries, RequestVote RPCs
│   │   └── log.go      # log management + persistence
│   ├── kv/             # KV service (dedup, leader fwd)
│   │   ├── server.go
│   │   └── client.go
│   └── storage/        # in-memory store + snapshot
│       ├── store.go
│       └── snapshot.go
├── api/                # HTTP handlers and router
├── config/             # configuration loading
└── docs/               # architecture and design docs
```

---

## 10. References

- Ongaro, D. & Ousterhout, J. (2014). [In Search of an Understandable Consensus Algorithm](https://raft.github.io/raft.pdf). USENIX ATC.
- Howard, H. (2016). [ARC: Analysis of Raft Consensus](https://www.cl.cam.ac.uk/techreports/UCAM-CL-TR-857.pdf). Cambridge Technical Report.
