# HTTP Client API Reference

go-distributed-kv exposes a simple JSON-over-HTTP API on each node.
Clients always talk to the **leader** for writes; reads can target any
node depending on the consistency mode requested.

Base URL: `http://<host>:<http-port>`  (default port: **8080**)

---

## Table of Contents

- [Authentication](#authentication)
- [Error format](#error-format)
- [Endpoints](#endpoints)
  - [PUT /kv/{key}](#put-kvkey)
  - [GET /kv/{key}](#get-kvkey)
  - [DELETE /kv/{key}](#delete-kvkey)
  - [GET /kv](#get-kv---list-all-keys)
  - [GET /status](#get-status)
  - [POST /cluster/join](#post-clusterjoin)
  - [DELETE /cluster/remove/{id}](#delete-clusterremoveid)
- [Consistency modes](#consistency-modes)
- [Forwarding to the leader](#forwarding-to-the-leader)
- [Client retry logic](#client-retry-logic)
- [Example: Go client](#example-go-client)

---

## Authentication

The API has no built-in authentication.  Run it behind a reverse proxy
(e.g. nginx, Envoy) if you need TLS termination or token-based auth.

---

## Error format

All 4xx and 5xx responses return a JSON body:

```json
{
  "error": "human-readable message",
  "code":  "SHORT_ERROR_CODE"
}
```

Common error codes:

| Code | HTTP status | Meaning |
|------|-------------|---------|
| `NOT_LEADER` | 307 | This node is not the leader; see `Location` header |
| `KEY_NOT_FOUND` | 404 | Key does not exist |
| `INVALID_BODY` | 400 | Malformed JSON request body |
| `PROPOSAL_DROPPED` | 503 | Raft proposal was dropped (leader step-down during write) |
| `TIMEOUT` | 504 | Proposal was not committed within the request deadline |
| `NO_LEADER` | 503 | Cluster has no elected leader; retry later |

---

## Endpoints

### PUT /kv/{key}

Write or overwrite a key.

**Request**

```
PUT /kv/foo HTTP/1.1
Content-Type: application/json

{"value": "bar"}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `value` | string | yes | Value to store (arbitrary UTF-8 string) |
| `ttl` | integer | no | Time-to-live in seconds; 0 = no expiry |

**Response 200 OK**

```json
{
  "key":   "foo",
  "value": "bar",
  "index": 42
}
```

`index` is the Raft log index at which the write was committed.
Clients can use this to implement read-your-writes by requesting
`?min_index=42` on subsequent GETs.

**Response 307 Temporary Redirect**

Returned when the request hits a follower.  The `Location` header
points to the current leader's HTTP address:

```
HTTP/1.1 307 Temporary Redirect
Location: http://node1:8080/kv/foo
```

---

### GET /kv/{key}

Read a key.

**Request**

```
GET /kv/foo HTTP/1.1
```

**Query parameters**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `consistency` | `linearizable` | `linearizable` or `stale` (see §Consistency modes) |
| `min_index` | 0 | Wait until the node's applied index ≥ min_index before serving |
| `timeout` | 5s | Maximum wait time for `min_index` (e.g. `2s`, `500ms`) |

**Response 200 OK**

```json
{
  "key":        "foo",
  "value":      "bar",
  "index":      42,
  "created_at": "2026-03-31T09:00:00Z",
  "updated_at": "2026-03-31T09:00:00Z"
}
```

**Response 404 Not Found**

```json
{"error": "key not found", "code": "KEY_NOT_FOUND"}
```

---

### DELETE /kv/{key}

Delete a key.

**Request**

```
DELETE /kv/foo HTTP/1.1
```

**Response 200 OK**

```json
{"key": "foo", "deleted": true, "index": 43}
```

**Response 404 Not Found** — key did not exist; the DELETE is still
committed to the log to maintain a consistent operation history.

---

### GET /kv — List all keys

Returns a paginated list of all keys.

**Query parameters**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `prefix` | `""` | Only return keys with this prefix |
| `limit` | 100 | Maximum number of keys per page |
| `cursor` | `""` | Pagination cursor from a previous response |
| `consistency` | `linearizable` | See §Consistency modes |

**Response 200 OK**

```json
{
  "keys": [
    {"key": "foo", "value": "bar",  "index": 42},
    {"key": "baz", "value": "qux",  "index": 37}
  ],
  "next_cursor": "baz",
  "total":       2
}
```

When `next_cursor` is empty, there are no more pages.

---

### GET /status

Returns the node's current Raft status.  Useful for health checks and
monitoring.

**Response 200 OK**

```json
{
  "id":             2,
  "role":           "leader",
  "term":           7,
  "leader_id":      2,
  "commit_index":   150,
  "applied_index":  150,
  "peers": [
    {"id": 1, "address": "node1:9090", "match_index": 149, "state": "replicate"},
    {"id": 3, "address": "node3:9092", "match_index": 150, "state": "replicate"}
  ],
  "snapshot": {
    "last_index": 100,
    "last_term":  5
  }
}
```

| Field | Description |
|-------|-------------|
| `role` | `leader`, `follower`, or `candidate` |
| `term` | Current Raft term |
| `commit_index` | Highest committed log index |
| `applied_index` | Highest applied log index (≤ commit_index) |
| `peers[].state` | `probe`, `replicate`, or `snapshot` |

---

### POST /cluster/join

Add a new node to the cluster.  Must be sent to the current leader.

**Request**

```json
{"id": 4, "raft_addr": "node4:9093", "http_addr": "node4:8083"}
```

**Response 200 OK**

```json
{"joined": true, "index": 151}
```

The new node should be started with the `--join` flag pointing to any
existing cluster member, which will redirect to the leader automatically.

---

### DELETE /cluster/remove/{id}

Remove a node from the cluster.  Must be sent to the current leader.
The removed node will step down and stop participating in consensus.

**Response 200 OK**

```json
{"removed": true, "id": 4, "index": 155}
```

---

## Consistency modes

| Mode | Guarantee | Latency | Notes |
|------|-----------|---------|-------|
| `linearizable` | Reads reflect all writes committed before the read | +1 RTT (ReadIndex) | Default; safe on all topologies |
| `stale` | May return slightly stale data | ~0 extra latency | Safe for cache-like workloads; reads served from follower's local state |

To use stale reads:

```
GET /kv/foo?consistency=stale HTTP/1.1
```

---

## Forwarding to the leader

When a follower receives a write or a linearisable read, it returns
`307 Temporary Redirect` with the leader's address in the `Location`
header.  Well-behaved clients should follow the redirect automatically
(most HTTP libraries do this by default) or cache the leader address
and retry directly.

---

## Client retry logic

Recommended retry strategy for writes:

1. Send the request.
2. On `307` — follow the redirect (update cached leader address).
3. On `503 NO_LEADER` or `503 PROPOSAL_DROPPED` — wait 100–500 ms and retry
   (a leader election is likely in progress).
4. On `504 TIMEOUT` — the write may or may not have been committed.
   Re-issue the write with an idempotency key if your application supports it.
5. On network error — retry with exponential backoff (max 3 attempts).

---

## Example: Go client

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type KVClient struct {
    leader string
    http   *http.Client
}

func NewKVClient(addr string) *KVClient {
    return &KVClient{
        leader: addr,
        http:   &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
            // update cached leader on redirect
            return nil
        }},
    }
}

func (c *KVClient) Put(key, value string) error {
    body, _ := json.Marshal(map[string]string{"value": value})
    resp, err := c.http.Post(
        fmt.Sprintf("http://%s/kv/%s", c.leader, key),
        "application/json",
        bytes.NewReader(body),
    )
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        var e map[string]string
        json.NewDecoder(resp.Body).Decode(&e)
        return fmt.Errorf("PUT failed: %s", e["error"])
    }
    return nil
}

func (c *KVClient) Get(key string) (string, error) {
    resp, err := c.http.Get(fmt.Sprintf("http://%s/kv/%s", c.leader, key))
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusNotFound {
        return "", fmt.Errorf("key not found")
    }
    var result map[string]interface{}
    json.NewDecoder(resp.Body).Decode(&result)
    return result["value"].(string), nil
}
```
