# Contributing

Pull requests are welcome.

## Running the Cluster Locally

```bash
go build ./...

# Terminal 1
go run main.go --id 1 --port 8001 --peers localhost:8002,localhost:8003

# Terminal 2
go run main.go --id 2 --port 8002 --peers localhost:8001,localhost:8003

# Terminal 3
go run main.go --id 3 --port 8003 --peers localhost:8001,localhost:8002
```

## Running Tests

```bash
go test ./...
go test ./raft/... -v -run TestElection
go test ./raft/... -v -run TestReplication
go test ./raft/... -v -run TestSnapshot
go test -race ./...   # check for data races
```

## Key Areas for Contribution

- **Read optimization** — serve reads from followers with lease-based consistency
- **Dynamic membership** — Raft joint consensus for adding/removing nodes
- **Metrics** — Prometheus instrumentation for leader elections and replication lag
- **Persistence** — replace in-memory log with a WAL backed by BadgerDB or bbolt

## Code Style

- `gofmt` + `go vet` before committing
- All Raft state transitions must be logged at `DEBUG` level
- No external dependencies beyond gRPC/protobuf
