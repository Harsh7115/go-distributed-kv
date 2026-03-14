package raft

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var logBucket = []byte("raft_log")

// PersistentLog is a durable, bbolt-backed append-only log for Raft entries.
// Each entry is keyed by its uint64 index stored as a big-endian 8-byte value
// so that bbolt's byte-ordered cursor gives us natural log ordering.
type PersistentLog struct {
	db *bolt.DB
}

// NewPersistentLog opens (or creates) the bbolt database at path and ensures
// the log bucket exists.
func NewPersistentLog(path string) (*PersistentLog, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open raft log db: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(logBucket)
		return err
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("create log bucket: %w", err)
	}
	return &PersistentLog{db: db}, nil
}

func indexKey(idx uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, idx)
	return k
}

// Append durably writes a single LogEntry.
func (l *PersistentLog) Append(entry LogEntry) error {
	return l.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(logBucket)
		val, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}
		return b.Put(indexKey(entry.Index), val)
	})
}

// AppendBatch durably writes multiple LogEntries in a single transaction.
func (l *PersistentLog) AppendBatch(entries []LogEntry) error {
	return l.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(logBucket)
		for _, e := range entries {
			val, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("marshal entry %d: %w", e.Index, err)
			}
			if err := b.Put(indexKey(e.Index), val); err != nil {
				return err
			}
		}
		return nil
	})
}

// Get retrieves the LogEntry at the given index.
func (l *PersistentLog) Get(index uint64) (LogEntry, error) {
	var entry LogEntry
	err := l.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(logBucket).Get(indexKey(index))
		if val == nil {
			return fmt.Errorf("log entry %d not found", index)
		}
		return json.Unmarshal(val, &entry)
	})
	return entry, err
}

// LastIndex returns the index of the most recent entry, or 0 if empty.
func (l *PersistentLog) LastIndex() (uint64, error) {
	var idx uint64
	err := l.db.View(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket(logBucket).Cursor().Last()
		if k != nil {
			idx = binary.BigEndian.Uint64(k)
		}
		return nil
	})
	return idx, err
}

// LastTerm returns the term of the most recent entry, or 0 if empty.
func (l *PersistentLog) LastTerm() (uint64, error) {
	var term uint64
	err := l.db.View(func(tx *bolt.Tx) error {
		_, v := tx.Bucket(logBucket).Cursor().Last()
		if v == nil {
			return nil
		}
		var e LogEntry
		if err := json.Unmarshal(v, &e); err != nil {
			return err
		}
		term = e.Term
		return nil
	})
	return term, err
}

// Slice returns all entries with index in [lo, hi).
func (l *PersistentLog) Slice(lo, hi uint64) ([]LogEntry, error) {
	var entries []LogEntry
	err := l.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(logBucket).Cursor()
		for k, v := c.Seek(indexKey(lo)); k != nil; k, v = c.Next() {
			idx := binary.BigEndian.Uint64(k)
			if idx >= hi {
				break
			}
			var e LogEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return nil
	})
	return entries, err
}

// TruncateAfter deletes all entries with index > after (inclusive conflict
// resolution when a follower detects a log mismatch).
func (l *PersistentLog) TruncateAfter(after uint64) error {
	return l.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(logBucket)
		c := b.Cursor()
		// Seek to the first key > after
		for k, _ := c.Seek(indexKey(after + 1)); k != nil; k, _ = c.Next() {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close releases the underlying bbolt database handle.
func (l *PersistentLog) Close() error {
	return l.db.Close()
}
