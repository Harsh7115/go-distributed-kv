# Observability

Monitoring and debugging go-distributed-kv in production.

---

## Metrics

The node exposes Prometheus-compatible metrics via HTTP on `:9090/metrics`.

### Raft Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `raft_term` | Gauge | Current Raft term |
| `raft_commit_index` | Gauge | Last committed log index |
| `raft_applied_index` | Gauge | Last applied log index |
| `raft_leader_changes_total` | Counter | Number of leader elections |
| `raft_heartbeat_duration_seconds` | Histogram | Time to send heartbeats |
| `raft_replication_lag` | Gauge | Follower replication lag (entries behind) |

### KV Store Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `kv_get_total` | Counter | Total Get operations |
| `kv_put_total` | Counter | Total Put operations |
| `kv_delete_total` | Counter | Total Delete operations |
| `kv_get_latency_seconds` | Histogram | Get operation latency |
| `kv_put_latency_seconds` | Histogram | Put operation latency |
| `kv_keys_total` | Gauge | Current number of keys in the store |
| `kv_snapshot_size_bytes` | Gauge | Size of the latest snapshot |

### Network Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `rpc_sent_total` | Counter | Total RPCs sent |
| `rpc_recv_total` | Counter | Total RPCs received |
| `rpc_errors_total` | Counter | RPC errors by type |
| `rpc_duration_seconds` | Histogram | RPC round-trip latency |

---

## Structured Logging

Logs are emitted in JSON format using `slog` (Go 1.21+):

```json
{
  "time": "2026-04-06T10:00:00Z",
  "level": "INFO",
  "msg": "became leader",
  "term": 4,
  "node_id": "node-1"
}
```

### Log Levels

- `DEBUG` — Raft tick events, heartbeat details, vote requests
- `INFO` — State transitions, snapshot creation, membership changes
- `WARN` — Slow follower detected, missed heartbeat
- `ERROR` — RPC failures, disk write errors, snapshot decode errors

Set log level at startup:

```bash
go-distributed-kv --log-level=debug
```

---

## Distributed Tracing

Spans are exported via OpenTelemetry to a Jaeger-compatible collector.

### Key Spans

```
client.Put
  └─ raft.Propose
       └─ raft.AppendEntries (fan-out to N followers)
            └─ raft.Commit
                 └─ kv.Apply
```

Configure the OTLP exporter:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_SERVICE_NAME=go-distributed-kv
go-distributed-kv --tracing=true
```

---

## Health Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /health/live` | Returns 200 when the process is running |
| `GET /health/ready` | Returns 200 when the node has joined the cluster |
| `GET /health/leader` | Returns 200 only on the current Raft leader |

---

## Alerting Rules (Prometheus)

```yaml
groups:
  - name: go-distributed-kv
    rules:
      - alert: NoLeader
        expr: sum(raft_leader_changes_total) == 0
        for: 30s
        annotations:
          summary: "Cluster has no leader"

      - alert: ReplicationLag
        expr: raft_replication_lag > 1000
        for: 1m
        annotations:
          summary: "Follower is > 1000 entries behind"
```
