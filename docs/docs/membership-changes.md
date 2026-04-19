# Cluster Membership Changes

This document explains how `go-distributed-kv` handles dynamic cluster membership — adding and removing nodes from a live Raft cluster without downtime.

---

## Why Membership Changes Are Tricky

In a static cluster, quorum is fixed: a 3-node cluster always needs 2 votes. If you naively switch from a 3-node to a 5-node configuration simultaneously, you can create a window where **two independent majorities** exist — one under the old config and one under the new — leading to split-brain.

The classic solution is **joint consensus** (Raft §6), where the cluster operates under a transitional configuration that requires a majority of both old and new members. This is safe but complex to implement.

This project uses the simpler **single-server approach**: only one node is added or removed at a time. With this restriction, the old and new quorums always overlap, and the safety problem disappears.

---

## The Single-Server Constraint

> At most one membership change is in flight at any time.

The leader enforces this by refusing to propose a new config entry until the previous one is committed. A config entry is committed when a majority of the *new* configuration acknowledges it.

---

## Adding a Node

### Step 1 — Catch-up phase

Before the new node receives any votes or participates in quorum, it must catch up with the leader's log. The leader replicates entries to the new peer as a **non-voting learner** for up to 10 rounds of `AppendEntries`. If the peer catches up within those rounds (its log lag drops below one election timeout worth of entries), it is promoted.

This prevents a slow new node from immediately stalling commit progress.

### Step 2 — Config entry

The leader appends a `CONFIG_ADD` log entry:

```go
type ConfigEntry struct {
    Type   ConfigChangeType // CONFIG_ADD or CONFIG_REMOVE
    NodeID uint64
    Addr   string           // gRPC address of the new peer
}
```

### Step 3 — Quorum shift

Once the `CONFIG_ADD` entry is committed by the **new** quorum (which includes the new node), the cluster operates under the updated membership. All subsequent `AppendEntries` and `RequestVote` calls use the new peer list.

### Step 4 — Leader redirect

Client writes during the catch-up phase are not stalled — they are committed by the current quorum. The new node simply applies them when it gets caught up.

---

## Removing a Node

### Removing a follower

1. Leader appends a `CONFIG_REMOVE` entry for the target node's ID.
2. Entry is committed by the new quorum (which no longer includes the removed node).
3. Leader stops sending `AppendEntries` to the removed node.
4. The removed node eventually times out, starts elections, and fails to win — it should be shut down.

> **Operator responsibility**: after issuing a removal, shut down the removed node's process. It will not affect the cluster once the config entry is committed, but it will consume network resources sending stale `RequestVote` RPCs.

### Removing the leader

Removing the currently serving leader is a special case:

1. Leader appends `CONFIG_REMOVE` for its own ID.
2. Once the entry is committed, the leader **steps down** and transitions to Follower.
3. A new election starts among the remaining nodes.
4. The ex-leader shuts itself down (or stops participating) after stepping down.

This means the cluster has no leader during the election, which adds one election-timeout (~150–300ms) of unavailability. This is acceptable for an infrequent operator action.

---

## API

Membership changes are driven via the HTTP management API (not the data API):

```bash
# Add a node
curl -X POST http://localhost:8080/cluster/members \
  -H 'Content-Type: application/json' \
  -d '{"id": 4, "addr": "localhost:9093"}'

# Remove a node
curl -X DELETE http://localhost:8080/cluster/members/4

# List current members
curl http://localhost:8080/cluster/members
# [
#   {"id":1,"addr":"localhost:9090","role":"leader"},
#   {"id":2,"addr":"localhost:9091","role":"follower"},
#   {"id":3,"addr":"localhost:9092","role":"follower"}
# ]
```

All write requests to non-leader nodes return HTTP 307 with a `Location` header pointing to the current leader.

---

## Failure Scenarios

| Scenario | Behaviour |
|----------|-----------|
| New node crashes during catch-up | Leader retries indefinitely; old quorum keeps committing |
| New node never catches up (slow disk) | Leader aborts the add after 10 rounds; returns error to operator |
| Leader crashes mid-add (before commit) | New leader re-proposes the config from its log (if entry was replicated to new leader) or the entry is lost (operator retries) |
| Network partition during remove | Removed node cannot win election (not in new config); partition heals safely |

---

## Linearizability Guarantee

Membership changes go through the Raft log, so they are linearizable with respect to data operations. A client that issues a write and then immediately queries cluster membership will see a state that is consistent with the write having been applied.

---

## References

- Ongaro & Ousterhout (2014), §4.1 — Single-server changes
- etcd documentation — [Runtime reconfiguration](https://etcd.io/docs/v3.5/op-guide/runtime-configuration/)
- Diego Ongaro's PhD thesis — Chapter 4 (extended discussion of joint consensus vs single-server)
