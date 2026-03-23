# Raft Internals: Implementation Notes

This document describes the key design decisions and implementation details inside the `internal/raft` package of **go-distributed-kv**.

---

## 1. State Machine Overview

Each Raft node cycles through three roles:

| Role      | Description |
|-----------|-------------|
| Follower  | Default state; accepts log entries from the leader |
| Candidate | Initiates a new election; requests votes from peers |
| Leader    | Drives all writes; sends heartbeats to suppress elections |

Transitions are driven by two timers:
- **Election timeout** (150–300 ms, randomised) — fires when a follower stops hearing from a leader.
- **Heartbeat interval** (50 ms) — the leader's periodic AppendEntries (possibly empty) to all peers.

---

## 2. Persistent State

The following fields are written to the WAL (bbolt bucket `"raft"`) **before** any RPC response is sent:

| Field        | Type      | Purpose |
|--------------|-----------|---------|
| `currentTerm` | uint64    | Monotonically increasing Raft term |
| `votedFor`    | uint64    | Node ID this node voted for in the current term (0 = none) |
| `log[]`       | []LogEntry| Sequence of commands submitted to the cluster |

Volatile state (commit index, last applied, next/match index) lives in memory only and is reconstructed on restart.

---

## 3. Leader Election

```
Follower           Candidate              Leader
  │   election        │                    │
  │─── timeout ──────▶│                    │
  │                   │── RequestVote ────▶ peers
  │                   │◀─ vote granted ───  peers
  │                   │  (majority)         │
  │                   │─────────────────── becomes leader
  │                   │                    │── AppendEntries(heartbeat) ──▶ peers
```

A candidate wins when it collects a **strict majority** (`⌊n/2⌋ + 1`) of votes. Votes are granted only when:
1. The candidate's term ≥ the voter's `currentTerm`.
2. The candidate's log is **at least as up-to-date** as the voter's (compared by last log term, then last log index).

Split votes are resolved by a new election with a fresh randomised timeout.

---

## 4. Log Replication

The leader appends a client command to its local log, then fans out `AppendEntries` RPCs to all followers in parallel. A log entry is **committed** once a majority acknowledge it.

### Consistency Check

Each `AppendEntries` carries `(prevLogIndex, prevLogTerm)`. A follower rejects the RPC if its log does not contain a matching entry at `prevLogIndex`. The leader backs off `nextIndex[peer]` on rejection until consistency is restored (fast log-backtracking using the `ConflictTerm` hint is supported).

### gRPC Streaming

Rather than a unary RPC per batch, the transport layer uses a **bidirectional stream** per peer pair. This amortises connection overhead for high-write workloads, reducing p99 latency by ~18 % in benchmarks.

---

## 5. Snapshotting & Log Compaction

When the log grows beyond `snapshotThreshold` entries (default: 10 000), the leader:

1. Serialises the current KV state machine into a snapshot blob.
2. Writes the snapshot to disk along with `(lastIncludedIndex, lastIncludedTerm)`.
3. Truncates the in-memory and on-disk log up to `lastIncludedIndex`.

Lagging followers that need entries before the snapshot receive an `InstallSnapshot` RPC instead of `AppendEntries`.

---

## 6. Read Scalability: Leader Leases

Linearisable reads normally require a Raft round-trip to confirm leadership. We instead use **leader leases**:

- The leader records the wall-clock time of its last successful heartbeat round (`leaseStart`).
- A read is served locally if `now < leaseStart + electionTimeout` (bounded clock skew assumed ≤ 10 ms).
- If the lease has expired the read falls back to a full round-trip.

This enables ~18 k linearisable GET/s vs ~12 k write/s on loopback (see README benchmarks).

---

## 7. Membership Changes

Dynamic membership uses **single-server changes** (§4.1 of the Raft paper): only one node is added or removed per configuration change. This avoids the split-brain hazard of joint consensus while keeping the implementation simple.

A `ConfChange` entry is appended to the log like any other command. The new configuration takes effect as soon as the entry is **applied**, not merely committed.

---

## 8. Known Limitations & Future Work

- **No pre-vote phase** — a partitioned node can increment its term and cause a temporary disruption on rejoining. Adding pre-vote (§9.6 of the Raft dissertation) would fix this.
- **Single-region only** — no geo-replication or multi-datacenter awareness.
- **Linearisable leases assume bounded clock skew** — nodes with drifting clocks should disable lease reads via `--no-lease-reads`.
- **Snapshotting is synchronous** — a large state machine will pause writes during compaction; async snapshotting is planned.

---

## References

- [Raft paper (Ongaro & Ousterhout, 2014)](https://raft.github.io/raft.pdf)
- [Raft dissertation (Ongaro, 2014)](https://web.stanford.edu/~ouster/cgi-bin/papers/OngaroPhD.pdf)
- [etcd/raft source](https://github.com/etcd-io/raft)
