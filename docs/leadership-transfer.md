# Leadership Transfer in go-distributed-kv

Raft's original paper describes leader election as a passive process: a follower
only becomes a candidate when its election timeout fires.  This works well for
fault tolerance, but it makes **graceful leader transfer** impossible without an
extension.  This document describes the leadership-transfer protocol used in
go-distributed-kv, when to use it, and the edge-cases it handles.

---

## Why Transfer Leadership?

Several operational scenarios require a deliberate handoff of the leader role:

| Scenario | Why Transfer? |
|---|---|
| Rolling upgrade | Drain the current leader before restarting it so the cluster stays available |
| Load rebalancing | Move the leader closer to the majority of clients |
| Planned maintenance | Evacuate a node before taking it offline |
| Preferred leader | Always keep a high-spec node as leader |

Without a controlled transfer, a restart of the leader causes a random re-election
that may pick any follower, often the slowest one.

---

## Protocol Overview

go-distributed-kv implements the **`TimeoutNow`** mechanism from §3.10 of the
Raft thesis (Ongaro 2014).  The sequence is:

```
Leader (L)               Target Follower (T)           Other Followers
    |                           |                              |
    |-- TimeoutNow(term) -----> |                              |
    |                           | election timeout fires NOW   |
    |                           |-- RequestVote(term+1) -----> |
    |                           |                 <-- granted -|
    |                           | becomes new leader           |
    | (steps down on seeing     |                              |
    |  higher term)             |                              |
```

### Step 1 — Drain in-flight log entries

Before sending `TimeoutNow`, the leader ensures the target is fully caught up.
It calls `waitForCatchup(targetID)`, which blocks until:

- `matchIndex[targetID] == lastLogIndex`, **or**
- a configurable timeout (default 2 × election-timeout) expires

If the timeout fires, the transfer is aborted with `ErrTransferTimeout`.

### Step 2 — Send TimeoutNow RPC

`TimeoutNow` is a one-shot RPC that instructs the target to immediately start a
new election, bypassing its election timer.  The leader atomically:

1. Sets `transferring = true` in its local Raft state.
2. Stops accepting new client writes (`Allow() → ErrLeaderTransferring`).
3. Sends `TimeoutNow` to the target.

### Step 3 — Target starts an election

The target:
1. Increments its current term.
2. Votes for itself.
3. Broadcasts `RequestVote` RPCs to all peers.
4. **Does NOT** check the pre-vote condition (see §Pre-Vote below) — the leader
   already certified the target is up-to-date.

### Step 4 — Leader steps down

When the old leader receives any message with a higher term (including the new
leader's first `AppendEntries`), it transitions to follower and clears
`transferring`.

---

## API

```go
// TransferLeadership initiates a graceful leadership transfer to targetID.
// It blocks until the transfer completes, the context is cancelled, or the
// deadline expires.
func (r *RaftNode) TransferLeadership(ctx context.Context, targetID uint64) error
```

### Error values

| Error | Meaning |
|---|---|
| `ErrNotLeader` | This node is not the current leader |
| `ErrUnknownPeer` | targetID is not in the current cluster membership |
| `ErrTransferTimeout` | Target did not catch up or win election in time |
| `ErrTransferAborted` | Another transfer was in progress; the new request preempted it |
| `context.DeadlineExceeded` | Caller's context expired |

### Example

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := raftNode.TransferLeadership(ctx, targetPeerID); err != nil {
    log.Printf("leadership transfer failed: %v", err)
    return err
}
log.Printf("leadership successfully transferred to node %d", targetPeerID)
```

---

## Pre-Vote and Leadership Transfer

go-distributed-kv also implements the **Pre-Vote** extension (§9.6 of the Raft
thesis), which normally requires a candidate to win a "dry-run" vote before
incrementing its term.  This prevents disruptions from partitioned nodes.

During a `TimeoutNow`-triggered election, the target **skips the pre-vote
round**, because:

- The current leader has already confirmed the target is up-to-date.
- Requiring pre-vote here would add an extra round trip and introduce a race
  where the target's pre-vote is rejected by followers who still see the old
  leader as healthy.

---

## Concurrent Transfers

Only one transfer may be active at a time.  If `TransferLeadership` is called
while a transfer is already in progress, the old transfer is aborted and the new
target is used.  The previous caller receives `ErrTransferAborted`.

---

## Interaction with Log Replication

While `transferring == true`:

- The leader rejects all client proposals with `ErrLeaderTransferring`.
- The leader continues forwarding already-committed entries (AppendEntries keep
  flowing so the target can catch up).
- Heartbeat RPCs are still sent to prevent unnecessary follower elections.

---

## Failure Modes and Recovery

### Target crashes before winning election

The leader detects the failed `TimeoutNow` when its own election timeout fires
(or when it sees no higher term emerge).  After `transferTimeout`, it:
1. Clears `transferring = false`.
2. Resumes accepting client writes.
3. Returns `ErrTransferTimeout` to the caller.

### Network partition isolates the target

Same as above — the timeout guards against indefinite blocking.

### Leader crashes mid-transfer

The in-progress transfer state is **not persisted** to the WAL.  After recovery,
the restarted node is a follower; the cluster elects a new leader via the normal
timeout-based mechanism.  The transfer is effectively a no-op.

---

## Configuration

```go
type RaftConfig struct {
    // TransferTimeout is the maximum duration the leader waits for the target
    // to catch up and win an election.  Defaults to 2 * ElectionTimeout.
    TransferTimeout time.Duration

    // ...other fields omitted for brevity...
}
```

---

## Testing

The integration test suite in `internal/raft/transfer_test.go` covers:

- Happy path: 3-node cluster, transfer from node 1 to node 2
- Target lagging: 1 000 uncommitted entries, transfer blocked until target catches up
- Target crash: transfer times out and leader resumes writes
- Concurrent requests: second caller gets `ErrTransferAborted`
- Context cancellation: mid-transfer cancel unblocks caller immediately

Run with:

```bash
go test -race -run TestLeaderTransfer ./internal/raft/...
```

---

## References

- Ongaro, D. (2014). *Consensus: Bridging Theory and Practice* (Raft thesis), §3.10, §9.6.
- etcd/raft: `node.go` — `TransferLeadership`, `checkQuorumActive`.
- TiKV: leadership transfer during region splits.
