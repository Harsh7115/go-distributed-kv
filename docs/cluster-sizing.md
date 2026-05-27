# Cluster Sizing Guide

Choosing the right cluster size for go-distributed-kv is the single most
impactful operational decision you will make. This document explains the
fault-tolerance maths, capacity planning heuristics, and upgrade/downgrade
procedures.

---

## 1. Quorum and Fault Tolerance

Raft requires a **majority quorum** (⌊N/2⌋ + 1 nodes) to make progress.
The table below shows how many simultaneous node failures each cluster size
can tolerate:

| Cluster size | Quorum needed | Failures tolerated | Notes |
|:------------:|:-------------:|:------------------:|-------|
| 1 | 1 | 0 | Dev/test only; no HA |
| 3 | 2 | **1** | Minimum HA configuration |
| 5 | 3 | **2** | Recommended for production |
| 7 | 4 | **3** | Large production or multi-AZ |
| 9 | 5 | 4 | Rarely needed; election cost rises |

> **Rule of thumb:** always use an **odd** number of nodes. An even-sized
> cluster (e.g. 4) tolerates the same failures as the next smaller odd size
> (3) but requires more resources and prolongs elections.

---

## 2. When to Choose Each Size

### 3-node cluster
- Single datacenter or two availability zones
- Can tolerate one AZ outage if nodes are spread
- Write throughput limited to one failed node at a time
- Best for: small production services, staging, internal tools

### 5-node cluster (recommended default)
- Spread across three AZs; survives loss of any one AZ plus one extra node
- Quorum survives rolling restarts without operator intervention
- Best for: customer-facing services, SLA-bound workloads

### 7-node cluster
- Four AZs or geographic distribution
- Use when regulatory requirements mandate cross-region replication
- Higher election timeout and heartbeat cost; tune `election_timeout_ms` up

---

## 3. Hardware Sizing

### CPU
Raft log replication is I/O-bound, not CPU-bound. 2–4 vCPUs per node is
sufficient for most workloads up to ~50k ops/s.

### Memory
Each log entry is buffered in memory until snapshotted. Estimate:

```
memory_needed ≈ snapshot_threshold × avg_entry_bytes × 2
```

For a snapshot threshold of 10,000 entries at 1 KB each:
- 10,000 × 1024 × 2 ≈ **20 MB** log buffer
- Add your KV store state-machine size on top

Start with **2–4 GB RAM** per node; tune `snapshot_threshold` down if memory
pressure appears.

### Disk
Use a local NVMe or SSD for the WAL. Network-attached storage (NAS/EFS) adds
latency that directly inflates p99 write times. Minimum recommended:

| Workload | Disk IOPS | Sequential write bandwidth |
|----------|-----------|---------------------------|
| Low (< 5k ops/s) | 3,000 | 100 MB/s |
| Medium (5–20k ops/s) | 10,000 | 250 MB/s |
| High (> 20k ops/s) | 30,000+ | 500+ MB/s |

### Network
- **Intra-cluster**: 1 Gbit LAN or better; latency < 1ms recommended
- **Election timeout**: set to at least 10× the 99th-percentile round-trip time
  between nodes to avoid spurious elections

---

## 4. Key Tuning Parameters

```sh
go run main.go \
  --id 1 \
  --peers localhost:8002,localhost:8003 \
  --heartbeat-interval-ms 150 \    # default 150ms
  --election-timeout-min-ms 300 \  # randomised between min and max
  --election-timeout-max-ms 500 \
  --snapshot-threshold 10000 \     # compact after N log entries
  --max-append-entries 64           # entries per AppendEntries RPC
```

| Parameter | Effect of increasing | Effect of decreasing |
|-----------|----------------------|----------------------|
| `heartbeat-interval-ms` | Lower network overhead | Faster failure detection |
| `election-timeout-*` | Fewer spurious elections | Faster leader failover |
| `snapshot-threshold` | Fewer disk writes | Lower memory and replay cost |
| `max-append-entries` | Higher batch throughput | Lower tail latency |

---

## 5. Scaling the Cluster

### Adding a node
1. Start the new node with `--learner` flag (does not vote until caught up).
2. Wait for its log to reach the current commit index (check
   `/metrics` → `raft_log_applied_index`).
3. Promote to voter via the membership-change API:
   ```sh
   kv-ctl member add --id 4 --addr localhost:8004
   ```
4. Verify quorum is healthy before proceeding.

### Removing a node
1. Issue the membership-change remove command:
   ```sh
   kv-ctl member remove --id 3
   ```
2. The cluster will reconfigure; the removed node will step down.
3. Shut down the node process after it acknowledges removal.

> Never remove a node without first checking that the remaining cluster size
> still meets your fault-tolerance requirement.

### Rolling restart (zero-downtime upgrade)
1. Restart followers one at a time; wait for each to rejoin before proceeding.
2. Transfer leadership away from the leader before restarting it:
   ```sh
   kv-ctl transfer-leadership --to 2
   ```
3. Restart the old leader; it will rejoin as a follower.

---

## 6. Monitoring Cluster Health

Key metrics to watch (exposed on `/metrics` in Prometheus format):

| Metric | Healthy value | Action if unhealthy |
|--------|--------------|---------------------|
| `raft_leader_id` | non-zero, stable | Investigate election churn |
| `raft_commit_lag` | < 100 entries | Check follower disk/network |
| `raft_election_count` | increases slowly | Tune election timeout up |
| `raft_snapshot_duration_seconds` | < 30s | Reduce snapshot threshold |
| `raft_applied_index` per node | converging | Network partition or crash |

Set alerts for any node where `raft_commit_lag > 1000` for more than 60 s.
