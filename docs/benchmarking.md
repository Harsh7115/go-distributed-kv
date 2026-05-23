# Benchmarking go-distributed-kv

This guide explains how to benchmark the distributed KV store — the Raft
layer, the client-facing API, and leader-failover recovery — so you can
measure throughput and latency in your environment.

---

## Prerequisites

- Go 1.21+
- A running 3-node cluster (see [operations.md](operations.md))

---

## 1. Built-in Go Benchmarks

```bash
go test ./... -bench=. -benchmem -count=3 | tee bench_$(date +%Y%m%d).txt
```

With CPU profiling:

```bash
go test ./internal/raft -bench=BenchmarkRaftApply \
    -cpuprofile=cpu.prof -memprofile=mem.prof
go tool pprof -http=:6060 cpu.prof
```

### Key benchmarks

| Benchmark | What it measures |
|-----------|-----------------|
| BenchmarkRaftApply | Log entry apply throughput (ops/sec) |
| BenchmarkRaftElection | Election convergence time |
| BenchmarkKVGet | Single-key read latency (leader + follower) |
| BenchmarkKVPut | Single-key write latency (leader) |
| BenchmarkKVBatchPut | Batch write throughput |
| BenchmarkSnapshotApply | Time to install a snapshot on a follower |

---

## 2. End-to-End Throughput Test

Start a 3-node cluster:

```bash
go run ./cmd/server --id 1 --peers 2@localhost:7002,3@localhost:7003 --port 8001 &
go run ./cmd/server --id 2 --peers 1@localhost:7001,3@localhost:7003 --port 8002 &
go run ./cmd/server --id 3 --peers 1@localhost:7001,2@localhost:7002 --port 8003 &
```

Run bulk load:

```bash
go run ./examples/bulk_load.go --addr localhost:8001 --n 10000 --concurrency 32
```

Expected output:

```
loaded 10000 keys in 1.23s  (8130 ops/sec)  p50=3.8ms  p99=12.1ms
```

---

## 3. Latency Percentile Measurement

```bash
go run ./examples/client_example.go \
    --addr localhost:8001 \
    --bench --ops 50000 --concurrency 16
```

Output:

```
ops=50000  concurrency=16  duration=6.2s
throughput: 8064 ops/sec
latency (put): p50=1.9ms  p95=4.7ms  p99=9.2ms  max=34ms
latency (get): p50=0.4ms  p95=1.1ms  p99=2.8ms  max=11ms
```

---

## 4. Leader Failover Benchmark

Kill the leader and measure re-election time:

```bash
kill $(lsof -ti:8001)
```

Watch the logs on remaining nodes for the new leader announcement.
Typical failover: **150–350 ms** (driven by election timeout).

To tighten:

```bash
go run ./cmd/server --election-timeout-min 100ms --election-timeout-max 200ms ...
```

---

## 5. Snapshot Cost

```bash
curl -X POST http://localhost:8001/admin/snapshot
```

Server log:

```
snapshot started  entries=125000  size_before=88MB
snapshot done     duration=430ms  size_after=12MB  ratio=7.3x
```

---

## 6. Baseline Numbers (Apple M2, local loopback)

| Scenario | Throughput | p99 latency |
|----------|-----------|-------------|
| Single-node (no Raft) | ~95 000 ops/s | 0.3 ms |
| 3-node cluster, writes | ~8 000 ops/s | 9 ms |
| 3-node cluster, reads (leader) | ~40 000 ops/s | 1.5 ms |
| Batch write (100 keys/batch) | ~55 000 kps | 4 ms |

> Network RTT dominates write latency. On a real LAN add ~0.5 ms per hop.

---

## 7. Regression Detection with benchstat

```bash
go install golang.org/x/perf/cmd/benchstat@latest

git checkout main
go test ./... -bench=. -count=5 > main.txt

git checkout my-branch
go test ./... -bench=. -count=5 > branch.txt

benchstat main.txt branch.txt
```

---

## See Also

- [performance-tuning.md](performance-tuning.md) — tuning knobs and config
- [raft-internals.md](raft-internals.md) — what drives Raft latency
- [operations.md](operations.md) — cluster setup and health monitoring
