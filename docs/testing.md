# Testing Guide

This document describes how to run, write, and interpret tests for
`go-distributed-kv`. It covers unit tests, integration tests, fault-injection
tests, and the race-detector workflow.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Running the Test Suite](#running-the-test-suite)
3. [Test Categories](#test-categories)
4. [Writing New Tests](#writing-new-tests)
5. [Fault Injection](#fault-injection)
6. [Race Detector](#race-detector)
7. [Continuous Integration](#continuous-integration)
8. [Debugging Flaky Tests](#debugging-flaky-tests)

---

## Prerequisites

- Go 1.22 or later (`go version` should report ≥ 1.22).
- No external services needed — the test harness spins up in-process nodes.

```bash
go version        # verify go ≥ 1.22
go mod download   # fetch all dependencies
```

---

## Running the Test Suite

### All tests (recommended before pushing)

```bash
go test ./... -count=1 -timeout 120s
```

The `-count=1` flag disables result caching so you always get a fresh run.

### Specific packages

```bash
# Raft layer only
go test ./internal/raft/... -v -timeout 60s

# KV server layer
go test ./kvserver/... -v -timeout 60s

# End-to-end client/cluster tests
go test ./tests/... -v -timeout 120s
```

### Running a single test by name

```bash
go test ./internal/raft/... -run TestLeaderElection -v -count=5
```

Running `-count=5` is useful for catching intermittent failures in timing-sensitive
election tests.

---

## Test Categories

### Unit Tests (`*_test.go` co-located with source)

Unit tests exercise individual functions and structs in isolation. They mock the
network transport so no real TCP connections are made.

Examples:
- `internal/raft/log_test.go` — log append, truncation, and term lookup.
- `internal/raft/vote_test.go` — `RequestVote` grant/deny logic.
- `internal/storage/storage_test.go` — WAL persistence and recovery.

### Integration Tests (`tests/` directory)

Integration tests bring up a full in-process cluster of 3 or 5 nodes using
`labrpc` (a simulated RPC layer that supports network partitions and message
delays). They verify end-to-end behaviour:

| Test file                   | What it covers                              |
|-----------------------------|---------------------------------------------|
| `tests/basic_test.go`       | Single Put/Get/Delete round-trips           |
| `tests/linearizability_test.go` | Jepsen-style linearizability checker   |
| `tests/partition_test.go`   | Cluster behaviour under network partitions  |
| `tests/snapshot_test.go`    | Log compaction and snapshot install         |
| `tests/concurrent_test.go`  | Many concurrent clients, no data races      |

### Benchmark Tests

```bash
go test ./... -bench=. -benchmem -benchtime=5s
```

Benchmark names follow the pattern `BenchmarkXxx`. Results are printed as
`ns/op`, `B/op`, and `allocs/op`.

---

## Writing New Tests

### Unit test skeleton

```go
package raft_test

import (
    "testing"

    "github.com/Harsh7115/go-distributed-kv/internal/raft"
)

func TestMyFeature(t *testing.T) {
    t.Parallel() // safe to run in parallel with other unit tests

    cfg := raft.NewTestConfig(3) // 3-node in-process cluster
    defer cfg.Cleanup()

    // ... exercise the feature ...

    if got := cfg.Leader(); got < 0 {
        t.Fatalf("expected a leader, got none")
    }
}
```

### Integration test skeleton

```go
package tests

import (
    "testing"

    "github.com/Harsh7115/go-distributed-kv/tests/harness"
)

func TestMyIntegration(t *testing.T) {
    h := harness.New(t, 3 /*nodes*/, false /*reliable network*/)
    defer h.Cleanup()

    clerk := h.MakeClerk()
    clerk.Put("key", "value")

    if v := clerk.Get("key"); v != "value" {
        t.Fatalf("Get returned %q, want %q", v, "value")
    }
}
```

### Conventions

- Test functions must start with `Test` (or `Benchmark` / `Fuzz` as appropriate).
- Use `t.Helper()` in assertion helpers so failure lines point to the caller.
- Avoid `time.Sleep` — use `cfg.WaitForLeader()` or poll with a timeout helper
  instead. Sleeps make tests slow and fragile.
- Clean up goroutines: always `defer cfg.Cleanup()` / `defer h.Cleanup()`.

---

## Fault Injection

The `labrpc` network simulator supports several failure modes you can enable in
tests:

```go
// Drop 30 % of messages between node 0 and node 1
h.Net.SetReliable(false)
h.Net.SetLongReorders(true)

// Partition: nodes {0,1} cannot talk to node {2}
h.Net.Partition([]int{0, 1}, []int{2})

// Heal the partition
h.Net.UnPartition()

// Crash node 1 (stops the goroutines, discards volatile state)
h.StopServer(1)

// Restart node 1 (restores from persistent state on disk)
h.StartServer(1)
```

When writing partition tests, always verify both that the majority partition
continues to make progress **and** that the minority partition does not commit
new entries.

---

## Race Detector

Always run at least a subset of tests with the race detector before merging:

```bash
go test -race ./... -count=1 -timeout 180s
```

The race detector adds ~5–10× overhead, so a 120 s timeout is extended to 180 s
above. The CI pipeline runs the race detector on every pull request.

Known limitations: `labrpc` intentionally uses goroutines that communicate via
channels; the race detector will not produce false positives here because the
channel operations act as synchronisation points.

---

## Continuous Integration

The repository uses GitHub Actions (see `.github/workflows/ci.yml`). On every
push and pull request the workflow:

1. Checks out the code.
2. Sets up Go 1.22.
3. Runs `go vet ./...` and `staticcheck ./...`.
4. Runs `go test ./... -race -count=1 -timeout 180s`.
5. Uploads a test result summary as a workflow artifact.

If a test fails in CI but passes locally, the most common causes are:

- Timing sensitivity — CI machines are slower and more variable. Increase timeouts
  or use `WaitForLeader()` instead of fixed sleeps.
- Goroutine leaks — the CI runner has limited resources. Use `goleak` in
  `TestMain` to detect leaked goroutines early.

---

## Debugging Flaky Tests

### Reproduce with high repetition

```bash
go test ./tests/... -run TestUnreliableNet -count=50 -timeout 600s 2>&1 | grep -E 'FAIL|PASS|panic'
```

### Enable verbose Raft logging

Set the `RAFT_DEBUG` environment variable to get per-node state-machine logs:

```bash
RAFT_DEBUG=1 go test ./internal/raft/... -run TestLeaderElection -v
```

### Capture the first failure

```bash
go test ./tests/... -run TestPartition -count=100 -timeout 600s -failfast 2>&1 | tee /tmp/test.log
```

The `-failfast` flag stops on the first test failure, preserving the log output
before subsequent tests overwrite it.

### Check for goroutine leaks

Add to `TestMain` in the package under test:

```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

---

*Last updated: April 2026*
