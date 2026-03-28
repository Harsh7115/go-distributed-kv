# Operations Guide

This guide covers deploying, running, and maintaining a `go-distributed-kv`
cluster in both development and production-like environments.

---

## Prerequisites

- Go 1.21 or later
- Three or more machines (or local ports) for a fault-tolerant cluster
- Open TCP ports between all nodes (default range: `7000`–`7002` for Raft,
  `8000`–`8002` for the client API)

---

## Building

```bash
git clone https://github.com/Harsh7115/go-distributed-kv
cd go-distributed-kv
go build -o kv-server ./cmd/server
```

The resulting `kv-server` binary is statically linked and has no runtime
dependencies beyond a Linux/macOS kernel.

---

## Starting a 3-node cluster

Each node needs to know its own ID, its own addresses, and the addresses
of every peer.  Launch all three in separate terminals (or as systemd
units — see below):

```bash
# Node 1
./kv-server \
  --id=1 \
  --raft-addr=127.0.0.1:7000 \
  --api-addr=127.0.0.1:8000 \
  --peers=2@127.0.0.1:7001,3@127.0.0.1:7002

# Node 2
./kv-server \
  --id=2 \
  --raft-addr=127.0.0.1:7001 \
  --api-addr=127.0.0.1:8001 \
  --peers=1@127.0.0.1:7000,3@127.0.0.1:7002

# Node 3
./kv-server \
  --id=3 \
  --raft-addr=127.0.0.1:7002 \
  --api-addr=127.0.0.1:8002 \
  --peers=1@127.0.0.1:7000,2@127.0.0.1:7001
```

Within a few seconds the nodes hold a leader election.  The elected leader
logs `[raft] became leader term=1`.

---

## Client operations

Use the built-in HTTP API (or the example client in `examples/client_example.go`).

### PUT a key

```bash
curl -X PUT http://127.0.0.1:8000/key/foo -d 'bar'
```

The request is automatically forwarded to the leader if it arrives at a
follower, so any node address works for writes.

### GET a key

```bash
curl http://127.0.0.1:8000/key/foo
# bar
```

Reads are served by the node that receives the request; for linearisable
reads, pass `?consistent=true` to force a round-trip through the leader.

### DELETE a key

```bash
curl -X DELETE http://127.0.0.1:8000/key/foo
```

---

## Cluster health

### Check leader

```bash
curl http://127.0.0.1:8000/status
```

Response example:

```json
{
  "nodeId": 1,
  "role": "leader",
  "term": 3,
  "commitIndex": 142,
  "peers": [
    {"id": 2, "addr": "127.0.0.1:7001", "matchIndex": 142},
    {"id": 3, "addr": "127.0.0.1:7002", "matchIndex": 141}
  ]
}
```

### Watch Raft log growth

```bash
watch -n1 'curl -s http://127.0.0.1:8000/status | jq .commitIndex'
```

---

## Handling node failures

A 3-node cluster tolerates **1 failure** (majority = 2).  A 5-node cluster
tolerates 2 failures.

When a follower crashes and restarts, it replays its persisted log and
rejoins the cluster automatically — no manual intervention needed.  The
leader will retransmit any missing entries.

If the **leader** crashes, the remaining nodes hold a new election within
`electionTimeout` (default: 150–300 ms randomised).  During that window
write requests will return a `503 Service Unavailable`; retry with
exponential back-off.

---

## Persistence and snapshots

Each node persists its Raft state (current term, voted-for, log entries)
to a write-ahead log in the data directory (`--data-dir`, default `./data`).

The server takes an automatic snapshot after every 1 000 committed log
entries to bound log growth.  You can trigger a manual snapshot via:

```bash
curl -X POST http://127.0.0.1:8000/snapshot
```

Log segments older than the latest snapshot are deleted automatically.

---

## Graceful shutdown

Send `SIGTERM` to let the node finish in-flight RPCs before exiting:

```bash
kill -TERM $(pgrep kv-server)
```

The node stops accepting new requests, drains pending log entries, and
flushes its WAL before exiting.  A `SIGKILL` is safe but skips draining.

---

## Running as a systemd service

```ini
[Unit]
Description=go-distributed-kv node %i
After=network.target

[Service]
ExecStart=/usr/local/bin/kv-server \
  --id=%i \
  --raft-addr=0.0.0.0:700%i \
  --api-addr=0.0.0.0:800%i \
  --data-dir=/var/lib/kv-node-%i \
  --peers=...  # fill in peer list
Restart=on-failure
RestartSec=2s
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

Instantiate with `systemctl enable kv-server@1`, `@2`, `@3`.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| All nodes stay follower | Split-brain / network partition | Check firewall rules between Raft ports |
| `503` on every write | No leader elected | Ensure majority of nodes are reachable |
| Log grows unbounded | Snapshots disabled | Check `--snapshot-interval` flag |
| Stale reads | Reading from follower without `?consistent=true` | Add `?consistent=true` or read from leader |
| Node won't rejoin | Corrupted WAL | Delete `--data-dir` to force full resync from leader |
