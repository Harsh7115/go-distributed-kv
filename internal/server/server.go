package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Harsh7115/go-distributed-kv/internal/store"
	"go.uber.org/zap"
)

// Server exposes the KV store over HTTP.
// All mutating requests (PUT, DELETE) are forwarded through the Raft log;
// GET requests are served directly from the local state machine.
type Server struct {
	addr    string
	kv      *store.KVStore
	propose func([]byte) (uint64, uint64, bool)
	logger  *zap.Logger
	mux     *http.ServeMux
	srv     *http.Server
}

// New builds a Server.
func New(addr string, kv *store.KVStore, propose func([]byte) (uint64, uint64, bool), log *zap.Logger) *Server {
	s := &Server{
		addr:    addr,
		kv:      kv,
		propose: propose,
		logger:  log,
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("/kv/", s.handleKV)
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.srv = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// Start begins listening on the configured address.
func (s *Server) Start() error {
	s.logger.Info("HTTP server listening", zap.String("addr", s.addr))
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleKV(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/kv/")
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r, key)
	case http.MethodPut:
		s.handlePut(w, r, key)
	case http.MethodDelete:
		s.handleDelete(w, r, key)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(w http.ResponseWriter, _ *http.Request, key string) {
	val, err := s.kv.Get(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"key": key, "value": val})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	cmd, err := store.EncodeCommand(store.OpPut, key, body.Value)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	idx, term, ok := s.propose(cmd)
	if !ok {
		http.Error(w, "not the leader — try another node", http.StatusServiceUnavailable)
		return
	}
	s.logger.Info("proposed put", zap.String("key", key), zap.Uint64("index", idx), zap.Uint64("term", term))
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{"index": idx, "term": term})
}

func (s *Server) handleDelete(w http.ResponseWriter, _ *http.Request, key string) {
	cmd, err := store.EncodeCommand(store.OpDelete, key, "")
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	idx, term, ok := s.propose(cmd)
	if !ok {
		http.Error(w, "not the leader — try another node", http.StatusServiceUnavailable)
		return
	}
	s.logger.Info("proposed delete", zap.String("key", key), zap.Uint64("index", idx), zap.Uint64("term", term))
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{"index": idx, "term": term})
}
