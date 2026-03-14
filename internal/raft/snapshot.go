package raft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Snapshot types
// ---------------------------------------------------------------------------

// Snapshot captures a consistent point-in-time image of the state machine so
// that old log entries can be compacted away (Raft §7).
type Snapshot struct {
	// Index is the last log index included in this snapshot.
	Index uint64 `json:"index"`
	// Term is the term of the entry at Index.
	Term uint64 `json:"term"`
	// Data is the opaque, application-defined serialised state.
	Data []byte `json:"data"`
}

// ---------------------------------------------------------------------------
// SnapshotStore — durable snapshot persistence
// ---------------------------------------------------------------------------

// SnapshotStore saves and loads the latest Raft snapshot to/from a file.
// Writes are atomic: new data is written to a temp file and then renamed,
// so a crash mid-write can never corrupt the last known-good snapshot.
type SnapshotStore struct {
	dir string
}

// NewSnapshotStore creates a SnapshotStore that persists snapshots under dir.
// The directory is created if it does not yet exist.
func NewSnapshotStore(dir string) (*SnapshotStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	return &SnapshotStore{dir: dir}, nil
}

func (s *SnapshotStore) path() string {
	return filepath.Join(s.dir, "snapshot.json")
}

// Save atomically persists snap to disk.
func (s *SnapshotStore) Save(snap *Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write snapshot tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	return nil
}

// Load reads and returns the most recently saved snapshot.
// Returns (nil, nil) when no snapshot has been saved yet.
func (s *SnapshotStore) Load() (*Snapshot, error) {
	data, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snap, nil
}

// Exists reports whether a snapshot has been saved.
func (s *SnapshotStore) Exists() bool {
	_, err := os.Stat(s.path())
	return err == nil
}
