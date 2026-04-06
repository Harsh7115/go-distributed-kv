# Troubleshooting Guide

This document covers the most common operational problems encountered when running
`go-distributed-kv` and explains how to diagnose and resolve them.

---

## Table of Contents

1. [Cluster won't form / nodes can't connect](#1-cluster-wont-form--nodes-cant-connect)
2. [No leader elected after timeout](#2-no-leader-elected-after-timeout)
3. [Writes are rejected with 503 / "not leader"](#3-writes-are-rejected-with-503--not-leader)
4. [Linearizable reads return stale data](#4-linearizable-reads-return-stale-data)
5. [Log grows unboundedly / snapshot not triggering](#5-log-grows-unboundedly--snapshot-not-triggering)
6. [Node crashes on restart with "corrupt log" error](#6-node-crashes-on-restart-with-corrupt-log-error)
7. [High p99 latency on PUT operations](#7-high-p99-latency-on-put-operations)
8. [Split-brain after network partition](#8-split-brain-after-network-partition)
9. [gRPC "connection refused" between peers](#9-grpc-connection-refused-between-peers)
10. [Race detector warnings in tests](#10-race-detector-warnings-in-tests)

---

## 1. Cluster Won't Form / Nodes Can't Connect

**Symptoms**

- Nodes start but log lines like `dialing peer 2: connection refused` repeat indefinitely.
- `/health` HTTP endpoint returns `503`.

**Causes & Fixes**

| Cause | Fix |
|-------|-----|
| Wrong `--peers` addresses | Double-check that each node's `--peers` flag lists the *Raft* addresses (not HTTP) of all *other* nodes. |
| Firewall blocking gRPC port | Open the port: `sudo ufw allow <raft-port>/tcp` |
| Nodes started in the wrong order | All peers must start within the election timeout window. Start all nodes within ~5 s of each other, or use a longer `--election-timeout`. |
| Mismatched cluster IDs | Every node in the same cluster must share the same `--cluster-id` (if configured). |

**Diagnostic commands**

```bash
# Check that the Raft port is listening
ss -tlnp | grep <raft-port>

# Attempt a manual gRPC connection
grpc_cli ls localhost:<raft-port>
```

---

## 2. No Leader Elected After Timeout

**Symptoms**

- All nodes remain in `Follower` or `Candidate` state for more than 30 s.
- Logs show repeated `starting election for term N` without ever reaching `became leader`.

**Causes & Fixes**

- **Even number of nodes** — Raft requires a majority. A 2-node cluster can never elect a leader after one failure. Always run 3, 5, or 7 nodes.
- **Clock skew too large** — If wall-clock skew between nodes exceeds the election timeout, leader leases may expire instantly. Ensure NTP is running: `chronyc tracking`.
- **Election timeout too short for the network RTT** — Increase `--election-timeout` so it is at least 10× the measured peer RTT.

```bash
# Measure peer RTT
ping -c 5 <peer-hostname>
```

---

## 3. Writes Are Rejected with 503 / "not leader"

**Symptoms**

```
HTTP 503 Service Unavailable
{"error": "not leader", "leader_hint": "localhost:8081"}
```

**Explanation**

Only the current Raft leader can accept writes. This is expected behaviour during a leader
transition (typically < 500 ms). The response body includes a `leader_hint` field pointing
to the current leader's HTTP address.

**Fix in client code**

```go
func putWithRedirect(nodes []string, key, value string) error {
    for _, addr := range nodes {
        resp, err := http.Post("http://"+addr+"/kv/"+key, "application/json",
            strings.NewReader(`{"value":"`+value+`"}`))
        if err != nil { continue }
        if resp.StatusCode == http.StatusOK { return nil }
        if resp.StatusCode == http.StatusServiceUnavailable {
            var body struct{ LeaderHint string `json:"leader_hint"` }
            json.NewDecoder(resp.Body).Decode(&body)
            if body.LeaderHint != "" {
                nodes = append([]string{body.LeaderHint}, nodes...)
            }
        }
    }
    return fmt.Errorf("no leader available")
}
```

---

## 4. Linearizable Reads Return Stale Data

**Symptoms**

A `PUT foo=bar` succeeds, but an immediate `GET foo` on a different node returns the old value.

**Explanation**

Follower nodes serve reads from their local state machine, which may lag behind the leader
by one or more log entries. This is *not* a bug — it is the defined behaviour of
non-linearizable (follower) reads.

**Fix**

Route all reads through the leader, or use the `?linearizable=true` query parameter which
forces a Raft round-trip before responding:

```bash
curl "http://localhost:8080/kv/foo?linearizable=true"
```

---

## 5. Log Grows Unboundedly / Snapshot Not Triggering

**Symptoms**

- `bbolt` database file grows without bound.
- Memory usage climbs steadily on long-running clusters.

**Causes & Fixes**

| Cause | Fix |
|-------|-----|
| `--snapshot-threshold` is set too high (or 0) | Set it to a reasonable value, e.g. `--snapshot-threshold=10000` (entries). |
| Snapshot goroutine panicking silently | Check logs for `snapshot failed:` lines; increase log verbosity with `--log-level=debug`. |
| Disk full — snapshot write fails | Free disk space; snapshots are written atomically to a temp file then renamed. |

```bash
# Check current log size
du -sh data/raft-log-<node-id>.db
```

---

## 6. Node Crashes on Restart with "Corrupt Log" Error

**Symptoms**

```
FATAL corrupt WAL: entry 1042 has wrong checksum (got 0xdeadbeef, want 0xabcd1234)
```

**Causes**

- Abrupt kill (SIGKILL / power loss) during a write — the WAL write was partially flushed.
- Disk I/O error.

**Recovery procedure**

1. Check `dmesg` or `journalctl -k` for I/O errors on the disk.
2. If no hardware fault, try replaying from the last valid snapshot:
   ```bash
   go run ./cmd/server --id=<id> --recover-from-snapshot
   ```
3. If the snapshot is also corrupt, remove the data directory and let the node re-sync from
   a healthy peer (it will be treated as a new, empty cluster member):
   ```bash
   rm -rf data/node-<id>/
   go run ./cmd/server --id=<id> ...
   ```
   The leader will detect the missing log entries and stream the latest snapshot.

---

## 7. High p99 Latency on PUT Operations

**Symptoms**

PUT p99 exceeds 50 ms even on a local loopback cluster.

**Common culprits**

| Culprit | Diagnosis | Fix |
|---------|-----------|-----|
| `fsync` on every log write | `strace -e fsync` shows frequent calls | Use `--sync-mode=batch` to group fsyncs |
| Large AppendEntries RPC batches serialising slowly | Increase `--max-append-entries` batch size | Tune `--max-append-entries=256` |
| GC pauses in Go runtime | `GODEBUG=gctrace=1` output | Pre-allocate buffers; use sync.Pool for RPC structs |
| Too many concurrent clients | Throughput saturated | Scale reads to followers; shard the key space |

---

## 8. Split-Brain After Network Partition

**Explanation**

Split-brain (two simultaneous leaders) is *impossible* by Raft's design as long as the
majority requirement is upheld. If you observe two nodes both claiming to be leader, it
almost certainly means:

- A stale leader has not yet detected it lost the election (it will step down within one
  heartbeat timeout once it receives a higher-term RPC).
- You have network routing misconfiguration causing asymmetric reachability.

**Verification**

```bash
# Query all nodes for their current role
for port in 8080 8081 8082; do
  echo -n "Node :$port => "
  curl -s http://localhost:$port/status | jq .role
done
```

At most one node should report `"leader"` at any point in time. If two do simultaneously,
capture their logs and open an issue with the term numbers shown.

---

## 9. gRPC "Connection Refused" Between Peers

**Symptoms**

```
rpc error: code = Unavailable desc = connection refused
```

**Checklist**

- [ ] Correct `--raft` address bound? (not `127.0.0.1` if peers are on different hosts)
- [ ] TLS mismatch? If one peer uses TLS and the other does not, connections are refused at the TLS handshake.
- [ ] Correct `--peers` format: `host:port` without a scheme prefix (`grpc://` is *not* valid here).

---

## 10. Race Detector Warnings in Tests

Running `go test -race ./...` may surface data races if you use the in-process test cluster
helpers without proper synchronisation.

**Known safe patterns**

```go
// Always wait for the cluster to stabilise before issuing requests
cluster.WaitForLeader(t, 5*time.Second)

// Use t.Cleanup to shut down nodes — not defer — so shutdown runs before race checks
t.Cleanup(func() { cluster.Shutdown() })
```

If you see a race in `internal/raft/node.go`, update to the latest commit — several
early races around the `commitIndex` field have been fixed.

---

## Getting More Help

- Open an issue at https://github.com/Harsh7115/go-distributed-kv/issues
- Include: Go version, OS, number of nodes, relevant log lines (with `--log-level=debug`)
- Attach the output of `go env` and `go version`
