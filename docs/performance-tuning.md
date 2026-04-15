# Performance Tuning Guide — go-distributed-kv

This guide covers the key levers for tuning throughput, latency, and resource usage
in a production `go-distributed-kv` cluster.

---

## Table of Contents

1. [Baseline Benchmarking](#baseline-benchmarking)
2. [Raft Timing Parameters](#raft-timing-parameters)
3. [Batching & Pipelining](#batching--pipelining)
4. [Snapshot Frequency](#snapshot-frequency)
5. [Network Tuning](#network-tuning)
6. [Memory & GC](#memory--gc)
7. [Storage Backend](#storage-backend)
8. [Read Scaling](#read-scaling)
9. [Monitoring Checklist](#monitoring-checklist)

---

## Baseline Benchmarking

Before changing anything, establish a baseline with the built-in benchmark harness:

```bash
# Run the default mixed-workload benchmark (3-node cluster, 10 k ops)
go test ./benchmark/... -bench=BenchmarkKV -benchtime=30s -count=3

# Target metrics to record
#   p50 / p99 / p999 write latency
#   writes/s and reads/s at saturation
#   leader CPU %, follower CPU %
#   heap allocations per op
```

Always benchmark with `GOMAXPROCS` set to the number of physical cores on your hardware
and with the profiler *off* (`-cpuprofile` adds measurable overhead).

---

## Raft Timing Parameters

The two most impactful parameters are the **heartbeat interval** and the
**election timeout range**.

| Parameter | Default | Effect |
|-----------|---------|--------|
| `HeartbeatMs` | 50 ms | Lower → faster failure detection, higher → less bandwidth |
| `ElectionTimeoutMinMs` | 150 ms | Must be >> `HeartbeatMs` |
| `ElectionTimeoutMaxMs` | 300 ms | Wider range → lower split-vote probability |

**Rules of thumb:**

- Keep `HeartbeatMs` at ≥ 2× your 99th-percentile cross-datacenter RTT.
- Keep `ElectionTimeoutMinMs` at ≥ 10× `HeartbeatMs` to avoid spurious elections
  under load.
- For LAN clusters (< 1 ms RTT) you can lower to 10 ms heartbeat / 50–100 ms
  election timeout for sub-100 ms failover.

```go
// config/raft.go
cfg := raft.Config{
    HeartbeatMs:          10,   // LAN-optimised
    ElectionTimeoutMinMs: 50,
    ElectionTimeoutMaxMs: 100,
}
```

---

## Batching & Pipelining

### Write Batching

Instead of one Raft round-trip per client request, the KV server can accumulate
pending ops and submit them as a single log entry:

```go
// kvserver/config.go
MaxBatchSize    = 64          // ops per batch
MaxBatchWaitMs  = 2           // max time to wait before flushing
```

Batching trades individual write latency (up by ≤ `MaxBatchWaitMs`) for higher
aggregate throughput — typically 3–8× improvement on workloads with high concurrency.

### AppendEntries Pipelining

The leader can send the next `AppendEntries` without waiting for the previous ACK
(bounded by `MaxInflightMsgs`). This hides network RTT on replication:

```go
MaxInflightMsgs = 16   // increase carefully; each slot costs memory
```

---

## Snapshot Frequency

Snapshots truncate the Raft log, bounding recovery time and disk use.

| Config | Default | Notes |
|--------|---------|-------|
| `SnapshotThresholdBytes` | 64 MiB | Trigger when log exceeds this size |
| `SnapshotThresholdEntries` | 100 000 | Trigger when entry count exceeds this |

Tuning guidance:

- **Too frequent:** snapshot I/O competes with normal writes; followers receive
  `InstallSnapshot` RPCs unnecessarily.
- **Too infrequent:** slow follower catch-up after restart; longer leader log scans.
- For write-heavy workloads, prefer entry-count triggers.
- For large-value workloads, prefer byte-size triggers.

Check snapshot duration in metrics:

```
raft_snapshot_duration_seconds{quantile="0.99"}  < 2.0   # target
```

---

## Network Tuning

### TCP Settings (Linux)

```bash
# Increase socket buffer sizes for high-throughput replication
sysctl -w net.core.rmem_max=134217728
sysctl -w net.core.wmem_max=134217728
sysctl -w net.ipv4.tcp_rmem="4096 87380 134217728"
sysctl -w net.ipv4.tcp_wmem="4096 65536 134217728"

# Enable TCP fast open for the Raft RPC port
sysctl -w net.ipv4.tcp_fastopen=3
```

### gRPC / net-rpc Connection Pool

Reuse persistent connections between nodes. The default transport already keeps
one connection per peer; make sure your load-balancer (if any) is configured for
long-lived TCP sessions (`proxy_read_timeout` ≥ 5 min in nginx).

---

## Memory & GC

The KV store is entirely in-memory. Peak heap usage is roughly:

```
heap ≈ store_size × 3   (live + log + snapshot copy during serialisation)
```

**Reduce GC pressure:**

```go
// Tune GOGC for lower GC frequency at the cost of more live heap
// GOGC=200 halves GC frequency with ~2× steady-state heap overhead
export GOGC=200

// Or set a hard limit to avoid OOM on noisy-neighbour hosts (Go 1.19+)
export GOMEMLIMIT=6GiB
```

Profile allocations with:

```bash
go test ./... -bench=. -memprofile=mem.out
go tool pprof -alloc_objects mem.out
```

Common hot spots: log entry serialisation (use `sync.Pool` for `gob.Encoder`),
map key copies (use fixed-size byte arrays where possible).

---

## Storage Backend

The default persistence layer uses `encoding/gob` over a write-ahead file.
For write-heavy workloads, switch to the optional `bbolt` backend:

```go
// cmd/server/main.go
store := persist.NewBbolt("data/raft.db", persist.BboltOptions{
    NoSync:         false,          // set true only in testing
    FreelistType:   bbolt.FreelistArrayType,
    InitialMmapSize: 1 << 30,      // 1 GiB initial mmap
})
```

`bbolt` uses B+ tree storage with `mmap`, which typically gives 2–4× higher
fsync throughput compared to the default append-only log on SSDs.

---

## Read Scaling

By default all reads go through the leader (linearisable). Two options to scale reads:

| Mode | Consistency | Latency | How to enable |
|------|-------------|---------|---------------|
| Linearisable (default) | Strong | RTT + apply delay | — |
| ReadIndex | Strong | ~0 extra overhead | `ReadMode: ReadIndex` |
| Follower reads | Eventual | Lowest | `ReadMode: Follower` |

ReadIndex allows the leader to confirm it is still the leader via a heartbeat quorum
before serving the read — same consistency as full Raft, but avoids writing to the log.

```go
cfg := kvserver.Config{
    ReadMode: kvserver.ReadIndex,
}
```

---

## Monitoring Checklist

Instrument the following counters in your metrics dashboard before going to production:

```
# Throughput
kv_ops_total{op="put|get|delete"}  rate over 1 m
raft_log_entries_committed_total    rate

# Latency
kv_op_duration_seconds{quantile="0.5|0.99|0.999"}
raft_append_entries_rtt_seconds{quantile="0.99"}

# Cluster health
raft_leader_changes_total           should be ~0 in steady state
raft_term                           should be stable
raft_follower_lag_entries           should stay < 1000

# Resource
process_resident_memory_bytes
go_gc_duration_seconds{quantile="0.99"}
```

Alert on:
- `raft_leader_changes_total` rate > 0.1/min (flapping leader)
- `raft_follower_lag_entries` > 50 000 (follower falling behind)
- `kv_op_duration_seconds{quantile="0.999"}` > 500 ms
