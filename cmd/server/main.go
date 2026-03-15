package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Harsh7115/go-distributed-kv/internal/app"
	"go.uber.org/zap"
)

func main() {
	id       := flag.Uint64("id",    1,        "node ID (unique within the cluster)")
	httpAddr := flag.String("http",  ":8080",  "HTTP API listen address")
	raftAddr := flag.String("raft",  ":9090",  "Raft RPC listen address")
	peers    := flag.String("peers", "",       `comma-separated peer list: "id=host:port,id=host:port"`)
	dataDir  := flag.String("data",  "./data", "directory for persistent Raft log storage")
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("logger: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	var peerList []string
	if *peers != "" {
		peerList = strings.Split(*peers, ",")
	}

	cfg := app.Config{
		NodeID:   *id,
		HTTPAddr: *httpAddr,
		RaftAddr: *raftAddr,
		Peers:    peerList,
		DataDir:  *dataDir,
	}

	node, err := app.New(cfg, logger)
	if err != nil {
		logger.Fatal("failed to initialise node", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := node.Run(ctx); err != nil {
		logger.Error("node exited with error", zap.Error(err))
		os.Exit(1)
	}
}
