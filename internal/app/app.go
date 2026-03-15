// Package app wires the Raft consensus layer, KV state machine, and HTTP API
// into a single runnable node. cmd/server/main.go delegates all construction
// here so the binary entrypoint stays thin.
package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Harsh7115/go-distributed-kv/internal/raft"
	"github.com/Harsh7115/go-distributed-kv/internal/server"
	"github.com/Harsh7115/go-distributed-kv/internal/store"
	"go.uber.org/zap"
)

// Config holds all tunables surfaced by the CLI flags.
type Config struct {
	NodeID   uint64
	HTTPAddr string   // address for the client-facing HTTP API
	RaftAddr string   // address this node advertises for Raft RPC
	Peers    []string // "id=host:port" entries for every *other* node
	DataDir  string
}

// ParsePeers splits "id=host:port" strings into parallel ID / address slices.
func ParsePeers(raw []string) (ids []uint64, addrs []string, err error) {
	for _, p := range raw {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("malformed peer %q: expected id=host:port", p)
		}
		id, e := strconv.ParseUint(parts[0], 10, 64)
		if e != nil {
			return nil, nil, fmt.Errorf("peer id %q: %w", parts[0], e)
		}
		ids = append(ids, id)
		addrs = append(addrs, parts[1])
	}
	return
}

// App is the fully-wired node: transport + Raft + KV store + HTTP API.
type App struct {
	cfg    Config
	node   *raft.Node
	kv     *store.KVStore
	srv    *server.Server
	trans  *raft.HTTPTransport
	logger *zap.Logger
}

// New constructs the App from cfg without starting any goroutines.
func New(cfg Config, logger *zap.Logger) (*App, error) {
	peerIDs, _, err := ParsePeers(cfg.Peers)
	if err != nil {
		return nil, err
	}

	// allIDs includes this node so quorum calculations are correct.
	allIDs := append([]uint64{cfg.NodeID}, peerIDs...)

	// applyCh is the bridge between Raft commits and the KV state machine.
	// A buffer of 256 avoids blocking the commit path under burst load.
	applyCh := make(chan []byte, 256)

	kv := store.New(applyCh)

	trans := raft.NewHTTPTransport(5 * time.Second)
	nodeCfg := raft.Config{
		ID:             cfg.NodeID,
		Peers:          allIDs,
		HeartbeatTick:  1,
		ElectionTick:   10,
		MaxLogEntries:  1000,
		SnapshotThresh: 10_000,
	}
	node := raft.NewNode(nodeCfg, trans, logger)
	node.SetApplyCh(applyCh)

	srv := server.New(cfg.HTTPAddr, kv, node.Propose, logger)

	return &App{
		cfg:    cfg,
		node:   node,
		kv:     kv,
		srv:    srv,
		trans:  trans,
		logger: logger,
	}, nil
}

// Run starts all background goroutines and blocks until ctx is cancelled or
// the HTTP server exits with an error.
func (a *App) Run(ctx context.Context) error {
	go func() {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				a.node.Tick()
			case <-ctx.Done():
				return
			}
		}
	}()

	srvErr := make(chan error, 1)
	go func() { srvErr <- a.srv.Start() }()

	a.logger.Info("node ready",
		zap.Uint64("id", a.cfg.NodeID),
		zap.String("http", a.cfg.HTTPAddr),
		zap.String("raft", a.cfg.RaftAddr),
	)

	select {
	case <-ctx.Done():
	case err := <-srvErr:
		return fmt.Errorf("HTTP server: %w", err)
	}

	return a.shutdown()
}

func (a *App) shutdown() error {
	a.logger.Info("shutting down gracefully")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.srv.Shutdown(shutCtx); err != nil {
		a.logger.Warn("HTTP server shutdown", zap.Error(err))
	}
	a.kv.Stop()
	_ = a.trans.Close()
	return nil
}
