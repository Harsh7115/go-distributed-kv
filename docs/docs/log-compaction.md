# Log Compaction & Snapshotting in go-distributed-kv

Raft's replicated log grows without bound unless periodically compacted.
This document explains how `go-distributed-kv` implements log compaction
via point-in-time snapshots, how snapshots are installed on lagging followers,
and what tuning knobs are available.

---

## Why Log Compaction Matters

Every committed entry — even a `del` that overwrites a previous `set` — stays
in the log forever unless something discards it.  Without compaction:

- **Memory**: the in-memory log grows proportionally to total write volume.
- **Restart time**: a node that crashes must replay the full log from index 0.
- **Follower catch-up**: a new or lagging follower must receive every entry
  since the beginning of time.

A snapshot captures the *state machine state* at a given log index, so all
entries up to that index can be safely discarded.

---

## Snapshot Format

A snapshot is a binary-encoded blob containing:

```
SnapshotHeader {
    LastIncludedIndex  uint64   // highest log index reflected in this snapshot
    LastIncludedTerm   uint64   // term of that entry
    ClusterConfig      []byte   // serialised membership config at that index
}
KVData []byte                   // gob-encoded map[string]string of the KV store
```

The header is fixed-width so a receiver can parse it before the (potentially
large) KV data payload arrives.

---

## Trigger Condition

Compaction runs in the background whenever:

```
commitIndex - snapshotIndex >= SnapshotThreshold
```

`SnapshotThreshold` is configured at node startup (default: **1000 entries**).
The check runs after every `AppendEntries` that advances `commitIndex`.

```go
// internal/raft/raft.go (simplified)
func (r *Raft) maybeSnapshot() {
    if r.commitIndex-r.snapshotIndex < uint64(r.cfg.SnapshotThreshold) {
        return
    }
    go r.takeSnapshot()
}
```

Running the snapshot in a goroutine avoids blocking the Raft event loop.

---

## Taking a Snapshot

```go
func (r *Raft) takeSnapshot() {
    r.mu.RLock()
    idx  := r.commitIndex
    term := r.log[idx - r.snapshotIndex - 1].Term
    data := r.fsm.Snapshot()   // serialise KV store
    r.mu.RUnlock()

    snap := &Snapshot{
        LastIncludedIndex: idx,
        LastIncludedTerm:  term,
        Data:              data,
    }
    r.snapStore.Save(snap)       // persist to disk

    r.mu.Lock()
    r.trimLog(idx)               // discard entries [0, idx]
    r.snapshotIndex = idx
    r.snapshotTerm  = term
    r.mu.Unlock()

    r.logger.Infof("snapshot taken at index %d", idx)
}
```

Key invariants maintained:
- `log[0]` always corresponds to `snapshotIndex + 1` after trimming.
- The snapshot is persisted **before** the in-memory log is trimmed so a
  crash between the two operations is safe.

---

## Installing a Snapshot on a Follower

When a follower's `nextIndex` falls below the leader's `snapshotIndex`,
the leader cannot send the missing entries (they have been compacted).
Instead it sends an **InstallSnapshot RPC**:

```
InstallSnapshotArgs {
    Term              uint64
    LeaderID          NodeID
    LastIncludedIndex uint64
    LastIncludedTerm  uint64
    Offset            uint64   // byte offset within the snapshot (chunked)
    Data              []byte   // chunk payload
    Done              bool     // true on the last chunk
}
```

### Chunked transfer

Snapshots can be arbitrarily large.  The leader splits the snapshot into
**64 KB chunks** and sends them sequentially.  The follower reassembles
them before applying.  If the connection drops mid-transfer, the leader
retries from `Offset = 0`.

### Follower application

```go
func (r *Raft) handleInstallSnapshot(args *InstallSnapshotArgs) *InstallSnapshotReply {
    // 1. Reject stale terms.
    if args.Term < r.currentTerm { ... }

    // 2. Accumulate chunks.
    r.snapBuf.Write(args.Data)
    if !args.Done { return &reply{Term: r.currentTerm} }

    // 3. Persist the full snapshot.
    snap := decodeSnapshot(r.snapBuf.Bytes())
    r.snapStore.Save(snap)

    // 4. Reset the state machine.
    r.fsm.Restore(snap.Data)

    // 5. Discard conflicting log entries.
    r.mu.Lock()
    r.log          = nil
    r.snapshotIndex = snap.LastIncludedIndex
    r.snapshotTerm  = snap.LastIncludedTerm
    r.commitIndex   = snap.LastIncludedIndex
    r.lastApplied   = snap.LastIncludedIndex
    r.mu.Unlock()

    r.snapBuf.Reset()
    return &reply{Term: r.currentTerm}
}
```

After step 4 the follower's KV state is identical to the leader's at
`LastIncludedIndex`.  Any subsequent `AppendEntries` entries pick up from
`LastIncludedIndex + 1`.

---

## Interaction with Leader Election

A newly elected leader does **not** take a snapshot immediately.  It first
establishes its authority (a no-op entry), then normal background compaction
resumes.

If a node that was a follower becomes leader while it has an older snapshot
than some peer, the new leader will serve `AppendEntries` from its log first;
only if a peer's `nextIndex` falls below the leader's `snapshotIndex` will it
switch to `InstallSnapshot`.

---

## Storage Layout

```
data/
  node-{id}/
    raft-state.bin     # persistent state (currentTerm, votedFor)
    log-{start}-{end}  # log segment files (future: segmented log)
    snapshot.bin       # latest snapshot
    snapshot.bin.tmp   # write-then-rename for crash safety
```

Write-then-rename ensures `snapshot.bin` is always either the old or new
complete snapshot — never a partial write.

---

## Configuration Reference

| Flag | Default | Description |
|------|---------|-------------|
| `--snapshot-threshold` | `1000` | Entries committed since last snapshot before triggering compaction |
| `--snapshot-chunk-size` | `65536` | Bytes per InstallSnapshot chunk |
| `--snapshot-dir` | `data/node-{id}` | Directory for snapshot and raft-state files |

Set `--snapshot-threshold` lower (e.g. `200`) for write-heavy workloads where
memory pressure matters more than compaction overhead.  Set it higher for
read-heavy workloads to reduce the cost of serialising the state machine.

---

## Performance Considerations

- **Snapshot serialisation** holds an `RLock` on the KV map for the duration
  of `fsm.Snapshot()`.  For large datasets consider copy-on-write schemes.
- **InstallSnapshot** does not count against heartbeat timeout; the leader
  continues sending heartbeats to other peers while the chunked transfer runs.
- **Concurrent snapshots** are prevented by a boolean flag; if a compaction
  is in progress when the threshold is crossed again, the new trigger is a
  no-op.
