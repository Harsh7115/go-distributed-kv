package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/zap"
)

func main() {
	id      := flag.Uint64("id", 1, "node ID (must be unique in cluster)")
	httpAddr := flag.String("http", ":8080", "HTTP API listen address")
	raftAddr := flag.String("raft", ":9090", "Raft gRPC listen address")
	peers    := flag.String("peers", "", "comma-separated list of peer raft addresses")
	dataDir  := flag.String("data", "./data", "directory for persistent Raft log storage")
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer logger.Sync()

	var peerList []string
	if *peers != "" {
		peerList = strings.Split(*peers, ",")
	}

	logger.Info("starting go-distributed-kv node",
		zap.Uint64("id", *id),
		zap.String("http", *httpAddr),
		zap.String("raft", *raftAddr),
		zap.Strings("peers", peerList),
		zap.String("dataDir", *dataDir),
	)

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		logger.Fatal("failed to create data dir", zap.Error(err))
	}

	// TODO: wire up raft.Node, store.KVStore, and server.Server here.
	// Keeping main lean — construction happens in an internal/app package.
	logger.Info("node ready", zap.String("http", *httpAddr))

	// Block until SIGINT / SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down gracefully")
}
