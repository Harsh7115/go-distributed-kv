# Raft Consensus Overview

This document explains how the Raft algorithm is implemented in **go-distributed-kv** to keep the replicated key-value state consistent across nodes.

## Why Raft?

Raft was designed to be more understandable than Paxos while providing the same fault-tolerance guarantees. Each cluster has at most one leader at any given term, and all writes flow through the leader. Followers replicate the leader's log and apply committed entries to the state machine in the same order.

## Roles

Every server is in exactly one of three states at any time:

- **Leader** — handles all client writes, replicates entries, sends periodic heartbeats.
- **Follower** — passive; responds to RPCs from the leader and candidates.
- **Candidate** — used during elections; transitions to leader on majority vote.

## Election Flow

1. A follower whose election timer expires increments its term and becomes a candidate.
2. The candidate votes for itself and sends `RequestVote` RPCs to peers.
3. A peer grants its vote if (a) it has not voted in this term and (b) the candidate's log is at least as up-to-date.
4. On a majority of votes the candidate becomes leader and immediately sends heartbeats to suppress new elections.
5. Split votes resolve via randomized election timeouts (150–300ms in our config).

## Log Replication

The leader appends each client command to its log and sends `AppendEntries` to followers. An entry is **committed** once it is stored on a majority of nodes; the leader then advances `commitIndex` and applies the entry to the state machine. Followers learn about the new commit index via the next heartbeat or AppendEntries.

## Safety Properties

Raft guarantees five core invariants:

- **Election Safety** — at most one leader per term.
- **Leader Append-Only** — a leader never overwrites its own log.
- **Log Matching** — if two logs share an entry at the same index and term, all preceding entries match.
- **Leader Completeness** — committed entries appear in every future leader's log.
- **State Machine Safety** — if a node has applied an entry at index i, no other node will ever apply a different entry at index i.

## Persistence

Before responding to any RPC, each node persists `currentTerm`, `votedFor`, and any new log entries to disk. We use a write-ahead log (WAL) format described in `docs/wal.md` (TODO).

## Snapshotting

Log truncation is handled via snapshots: when the log exceeds 10k entries the leader produces a snapshot of the state machine and ships it to lagging followers using `InstallSnapshot` RPC. Snapshots include the last applied index and term so receivers can splice them into their log correctly.

## References

- Diego Ongaro & John Ousterhout, *In Search of an Understandable Consensus Algorithm* (Raft paper).
- The Raft visualization at https://raft.github.io for a hands-on intuition pump.
