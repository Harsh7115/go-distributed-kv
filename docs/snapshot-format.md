# Snapshot Format

This document specifies the on-disk and on-the-wire format used for Raft snapshots
in `go-distributed-kv`. Snapshots are how a node bounds the size of its log: once a
snapshot at index `N` has been committed and persisted, all log entries with index
`<= N` may be discarded.

## When snapshots are taken

Each replica counts the number of applied log entries since its last snapshot.
When the count exceeds `SnapshotThreshold` (default `8192`), the apply loop schedules
a snapshot:

1. The state machine is asked for a serialized copy of its current state.
2. The current `LastApplied` index and term are recorded.
3. A new snapshot file is written atomically: data is staged in a `.tmp` file in the
   same directory, fsynced, then renamed over the previous snapshot.
4. The Raft log is truncated up to and including the snapshot's last index.

Snapshotting runs concurrently with normal request processing; the apply loop only
blocks while the state machine is producing the byte stream.

## File layout

Each snapshot is one file. Bytes are big-endian unless noted.

```
+-------------------+----------+----------------------------------------+
| Offset            | Length   | Field                                  |
+-------------------+----------+----------------------------------------+
| 0                 | 4        | magic = 0x52534E50 ("RSNP")            |
| 4                 | 2        | format version (currently 1)           |
| 6                 | 2        | reserved, must be zero                 |
| 8                 | 8        | last included index (uint64)           |
| 16                | 8        | last included term (uint64)            |
| 24                | 4        | configuration length C (uint32)        |
| 28                | C        | configuration blob (JSON)              |
| 28 + C            | 8        | state machine payload length S         |
| 36 + C            | S        | state machine payload (opaque bytes)   |
| 36 + C + S        | 4        | CRC-32C of all preceding bytes         |
+-------------------+----------+----------------------------------------+
```

The CRC is verified on load. A mismatch causes the file to be rejected and the
node to fall back to log replay from the previous snapshot, if any.

## Configuration blob

The configuration blob is a JSON document describing the cluster membership at the
time of the snapshot:

```json
{
  "members": [
    {"id": "node-1", "addr": "10.0.0.11:7000"},
    {"id": "node-2", "addr": "10.0.0.12:7000"},
    {"id": "node-3", "addr": "10.0.0.13:7000"}
  ],
  "voters": ["node-1", "node-2", "node-3"]
}
```

Storing the configuration with the snapshot means a node restoring from snapshot
does not need to consult the (possibly truncated) log to learn cluster membership.

## State machine payload

The payload is opaque to the Raft layer. The default key-value state machine in this
repository serializes its in-memory map using gob with a stable schema:

```go
type kvSnapshot struct {
    Pairs   map[string][]byte
    Tombstones map[string]uint64
    LastTxnID uint64
}
```

Replacing the state machine with a different implementation only requires that the
new implementation can serialize and restore from an arbitrary `[]byte`.

## InstallSnapshot RPC

Snapshots are shipped to a follower that has fallen too far behind for log
replication via the `InstallSnapshot` RPC. The RPC streams the snapshot file in
fixed-size chunks (`SnapshotChunkSize`, default 1 MiB) so that very large snapshots
do not require holding the full payload in memory on either side. The receiver writes
chunks into a `.tmp` file and only renames it into place after receiving the final
chunk and verifying the CRC.

## Compatibility

- A node started with a binary that does not understand the file's `format version`
  refuses to load the snapshot and exits with a clear error rather than silently
  reformatting it.
- Snapshot files are forward-compatible within a major version: nodes ignore
  trailing fields they do not recognize, provided the CRC still validates.
