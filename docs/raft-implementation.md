# Raft Implementation Notes

This document describes the key design decisions and tricky corner cases in the Raft consensus implementation inside `internal/raft/`.

---

## 1. State Machine Overview

Each node runs as a state machine with three possible roles:

| Role      | Responsibilities |
|-----------|-----------------|
| Follower  | Accept log entries from leader; vote in elections |
| Candidate | Solicit votes; become leader on majority |
| Leader    | Accept client writes; replicate log; send heartbeats |

State transitions are driven by timers and incoming RPC messages. The core loop lives in `node.go` and is deliberately single-threaded to avoid races on Raft state.

---

## 2. Persistent State

Per the Raft paper, three fields must be flushed to stable storage before responding to any RPC:

```go
type PersistentState struct {
    CurrentTerm uint64      // latest term server has seen
    VotedFor    *uint64     // candidateId that received vote in current term (nil if none)
    Log         []LogEntry  // log entries; each contains command and term received by leader
}
```

We use **bbolt** (a key-value store backed by a memory-mapped B+tree) to persist these atomically. A single bbolt `Update` transaction writes all three fields together, so there is no partial-write hazard on crash.

---

## 3. Leader Election

### Election timer
Each follower randomises its election timeout in `[T, 2T]` (default T = 150 ms). On timeout it:
1. Increments `CurrentTerm`
2. Transitions to Candidate
3. Votes for itself
4. Sends `RequestVote` RPCs to all peers in parallel

### Vote grant conditions (§5.2 and §5.4)
A node grants a vote only if:
- `args.Term >= currentTerm`
- It has not already voted in this term (or voted for this candidate)
- The candidate's log is at least as up-to-date as the voter's log:
  - Higher last log term wins; on tie, longer log wins

### Split-vote avoidance
The randomised timeout window makes simultaneous elections rare. If a split vote occurs, all candidates time out and start a new election — no livelock is possible as long as timeouts are random.

---

## 4. Log Replication

### AppendEntries RPC
Leaders send `AppendEntries` to all followers on every tick (heartbeat) and immediately on new client writes. The RPC carries:
- `prevLogIndex` / `prevLogTerm`: consistency check
- `entries[]`: new entries to append (empty for heartbeat)
- `leaderCommit`: leader's current commit index

Followers reject the RPC if their log does not match at `prevLogIndex`. The leader backs down `nextIndex[peer]` by one on each rejection (slow roll-back). The optimisation described in the paper (fast roll-back by term) is implemented in `transport.go` via the `ConflictTerm` / `ConflictIndex` hint fields.

### Commit rule (§5.3)
A log entry is committed only when stored on a **majority** of nodes **in the current term**. This prevents the "figure 8" anomaly where entries from previous terms are incorrectly committed.

```go
// Advance commitIndex only for entries in currentTerm
for n := rf.commitIndex + 1; n <= rf.lastLogIndex(); n++ {
    if rf.log[n].Term != rf.currentTerm {
        continue
    }
    count := 1 // self
    for _, matchIdx := range rf.matchIndex {
        if matchIdx >= n {
            count++
        }
    }
    if count > len(rf.peers)/2 {
        rf.commitIndex = n
    }
}
```

---

## 5. Log Compaction (Snapshotting)

When the log exceeds `SnapshotThreshold` entries, the leader instructs the state machine to take a snapshot. The snapshot contains:

- `LastIncludedIndex`: last log index reflected in the snapshot
- `LastIncludedTerm`: term at that index
- `Data`: serialised KV store state

After snapshotting, all log entries up to `LastIncludedIndex` are discarded. Followers that are far behind receive the snapshot via an `InstallSnapshot` RPC rather than individual log entries.

---

## 6. Leader Leases for Linearizable Reads

Reading through the Raft log (appending a no-op read command) is correct but expensive. We use **leader leases** as an optimisation:

A leader is guaranteed to be the unique leader for a bounded window `[now, now + lease_duration]` if:
1. It received heartbeat ACKs from a quorum within the last `election_timeout` window
2. Clocks are loosely synchronised (bounded skew < `lease_duration`)

When both hold, the leader can serve GET requests directly from its local state machine without a round-trip, reducing read latency by ~60% in benchmarks.

**Risk**: if clock skew exceeds assumptions, stale reads are possible. The feature is therefore gated behind a `--enable-lease-reads` flag (off by default).

---

## 7. Membership Changes

Membership changes use **single-server changes** (one node at a time) per §6 of the paper. This avoids the "disjoint majority" problem that arises with joint consensus if implemented incorrectly.

Adding a node:
1. New node starts as a **non-voting learner** and replicates the full log.
2. Once caught up (lag < one round-trip), leader appends a `AddNode` configuration entry.
3. Once committed, the new node participates in quorums.

Removing a node:
1. Leader appends a `RemoveNode` entry.
2. Once committed, the removed node stops responding to RPCs.

---

## 8. gRPC Transport

All inter-node communication uses gRPC (defined in `proto/raft.proto`). Key choices:

- **Bidirectional streaming** for `AppendEntries` to amortise connection setup cost on high-write workloads.
- **Unary RPC** for `RequestVote` and `InstallSnapshot` (infrequent; simplicity wins).
- Deadline set to `election_timeout / 2` on all RPCs so a slow peer cannot stall the leader.

---

## 9. Known Limitations

- **No pre-vote phase**: a partitioned node with a high term can disrupt the cluster when it rejoins. The pre-vote extension (§9.6) would prevent this and is a planned addition.
- **In-memory log during tests**: the bbolt persistence layer is bypassed in unit tests via a `MemStorage` interface, which means crash-recovery paths are not covered by the current test suite.
- **Single-region only**: the current implementation assumes a LAN environment. Cross-datacenter latency would require tuning timeouts significantly.
