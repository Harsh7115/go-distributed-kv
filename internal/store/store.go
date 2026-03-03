package store

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Op represents the type of KV operation.
type Op uint8

const (
	OpPut Op = iota
	OpDelete
)

// Command is the payload written into the Raft log.
type Command struct {
	Op    Op     `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// KVStore is a thread-safe, in-memory key-value store that acts as
// the Raft state machine. It applies committed log entries in order.
type KVStore struct {
	mu      sync.RWMutex
	data    map[string]string
	applyCh <-chan []byte
	stopCh  chan struct{}
}

// New creates a KVStore and starts the background apply loop.
func New(applyCh <-chan []byte) *KVStore {
	s := &KVStore{
		data:    make(map[string]string),
		applyCh: applyCh,
		stopCh:  make(chan struct{}),
	}
	go s.applyLoop()
	return s
}

// Get returns the value for key, or an error if it doesn't exist.
func (s *KVStore) Get(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return "", fmt.Errorf("key not found: %q", key)
	}
	return v, nil
}

// Snapshot returns a deep copy of the current store state.
func (s *KVStore) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := make(map[string]string, len(s.data))
	for k, v := range s.data {
		snap[k] = v
	}
	return snap
}

// Restore replaces the store state from a snapshot.
func (s *KVStore) Restore(snap map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = snap
}

// Len returns the number of stored keys.
func (s *KVStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Stop shuts down the apply goroutine.
func (s *KVStore) Stop() { close(s.stopCh) }

func (s *KVStore) applyLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		case raw, ok := <-s.applyCh:
			if !ok {
				return
			}
			s.apply(raw)
		}
	}
}

func (s *KVStore) apply(raw []byte) {
	var cmd Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return // malformed entry; skip
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch cmd.Op {
	case OpPut:
		s.data[cmd.Key] = cmd.Value
	case OpDelete:
		delete(s.data, cmd.Key)
	}
}

// EncodeCommand serialises a command to JSON bytes for the Raft log.
func EncodeCommand(op Op, key, value string) ([]byte, error) {
	return json.Marshal(Command{Op: op, Key: key, Value: value})
}
