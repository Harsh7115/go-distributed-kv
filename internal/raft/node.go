package raft

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// NodeState represents the current role of a Raft node.
type NodeState int32

const (
	Follower NodeState = iota
	Candidate
	Leader
)

func (s NodeState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	default:
		return "unknown"
	}
}

// LogEntry is a single entry in the replicated log.
type LogEntry struct {
	Index   uint64
	Term    uint64
	Command []byte
}

// Config holds the configuration for a Raft node.
type Config struct {
	ID              uint64
	Peers           []uint64
	HeartbeatTick   int
	ElectionTick    int
	MaxLogEntries   int
	SnapshotThresh  int
}

// Node is a single Raft participant.
type Node struct {
	mu sync.RWMutex

	id      uint64
	state   atomic.Int32
	term    uint64
	votedFor uint64
	log     []LogEntry

	commitIndex uint64
	lastApplied uint64

	// Leader-only state
	nextIndex  map[uint64]uint64
	matchIndex map[uint64]uint64

	// Election timer
	electionTimeout  int
	electionElapsed  int
	heartbeatTimeout int
	heartbeatElapsed int

	votes map[uint64]bool

	// applyCh receives the raw command bytes of every newly committed entry.
	// Set via SetApplyCh; nil means no delivery (useful in tests).
	applyCh chan<- []byte

	transport Transport
	logger    *zap.Logger
	config    Config
}

// NewNode creates a new Raft node with the given config.
func NewNode(cfg Config, transport Transport, logger *zap.Logger) *Node {
	n := &Node{
		id:               cfg.ID,
		config:           cfg,
		transport:        transport,
		logger:           logger,
		electionTimeout:  cfg.ElectionTick + rand.Intn(cfg.ElectionTick),
		heartbeatTimeout: cfg.HeartbeatTick,
		nextIndex:        make(map[uint64]uint64),
		matchIndex:       make(map[uint64]uint64),
		votes:            make(map[uint64]bool),
	}
	n.state.Store(int32(Follower))
	return n
}

// State returns the current role of the node.
func (n *Node) State() NodeState {
	return NodeState(n.state.Load())
}

// Term returns the current term.
func (n *Node) Term() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.term
}

// SetApplyCh registers a channel that will receive the Command bytes of each
// committed log entry in order. Must be called before the first Tick.
func (n *Node) SetApplyCh(ch chan<- []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.applyCh = ch
}

// Tick advances the internal logical clock by one tick.
// Callers should invoke Tick at a regular interval (e.g. every 10ms).
func (n *Node) Tick() {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch n.State() {
	case Follower, Candidate:
		n.electionElapsed++
		if n.electionElapsed >= n.electionTimeout {
			n.startElection()
		}
	case Leader:
		n.heartbeatElapsed++
		if n.heartbeatElapsed >= n.heartbeatTimeout {
			n.heartbeatElapsed = 0
			n.broadcastHeartbeat()
		}
	}
}

// Propose submits a command to the Raft log. Only valid on the leader.
func (n *Node) Propose(cmd []byte) (uint64, uint64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.State() != Leader {
		return 0, 0, false
	}

	entry := LogEntry{
		Index:   n.lastLogIndex() + 1,
		Term:    n.term,
		Command: cmd,
	}
	n.log = append(n.log, entry)
	n.logger.Info("proposed entry", zap.Uint64("index", entry.Index), zap.Uint64("term", entry.Term))

	// Optimistically replicate to followers
	go n.replicateLog()

	return entry.Index, entry.Term, true
}

func (n *Node) startElection() {
	n.term++
	n.state.Store(int32(Candidate))
	n.votedFor = n.id
	n.votes = map[uint64]bool{n.id: true}
	n.electionElapsed = 0
	n.electionTimeout = n.config.ElectionTick + rand.Intn(n.config.ElectionTick)

	n.logger.Info("starting election", zap.Uint64("term", n.term))

	lastIdx := n.lastLogIndex()
	lastTerm := n.lastLogTerm()
	for _, peer := range n.config.Peers {
		if peer == n.id {
			continue
		}
		go n.transport.SendRequestVote(peer, &RequestVoteArgs{
			Term:         n.term,
			CandidateID:  n.id,
			LastLogIndex: lastIdx,
			LastLogTerm:  lastTerm,
		})
	}
}

func (n *Node) becomeLeader() {
	n.state.Store(int32(Leader))
	n.heartbeatElapsed = 0

	// Initialize leader state
	nextIdx := n.lastLogIndex() + 1
	for _, peer := range n.config.Peers {
		n.nextIndex[peer] = nextIdx
		n.matchIndex[peer] = 0
	}
	n.logger.Info("became leader", zap.Uint64("term", n.term))

	// Send immediate heartbeat
	n.broadcastHeartbeat()
}

func (n *Node) broadcastHeartbeat() {
	for _, peer := range n.config.Peers {
		if peer == n.id {
			continue
		}
		go n.sendAppendEntries(peer, nil) // empty = heartbeat
	}
}

func (n *Node) replicateLog() {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for _, peer := range n.config.Peers {
		if peer == n.id {
			continue
		}
		go n.sendAppendEntries(peer, n.entriesForPeer(peer))
	}
}

func (n *Node) sendAppendEntries(peer uint64, entries []LogEntry) {
	prevIdx := n.nextIndex[peer] - 1
	prevTerm := uint64(0)
	if prevIdx > 0 && int(prevIdx-1) < len(n.log) {
		prevTerm = n.log[prevIdx-1].Term
	}

	n.transport.SendAppendEntries(peer, &AppendEntriesArgs{
		Term:         n.term,
		LeaderID:     n.id,
		PrevLogIndex: prevIdx,
		PrevLogTerm:  prevTerm,
		Entries:      entries,
		LeaderCommit: n.commitIndex,
	})
}

func (n *Node) entriesForPeer(peer uint64) []LogEntry {
	next := n.nextIndex[peer]
	if int(next-1) >= len(n.log) {
		return nil
	}
	return n.log[next-1:]
}

func (n *Node) lastLogIndex() uint64 {
	if len(n.log) == 0 {
		return 0
	}
	return n.log[len(n.log)-1].Index
}

func (n *Node) lastLogTerm() uint64 {
	if len(n.log) == 0 {
		return 0
	}
	return n.log[len(n.log)-1].Term
}

// maybeCommit advances commitIndex if a quorum has replicated an entry.
func (n *Node) maybeCommit() {
	quorum := (len(n.config.Peers) / 2) + 1
	for idx := n.commitIndex + 1; idx <= n.lastLogIndex(); idx++ {
		count := 1
		for _, peer := range n.config.Peers {
			if peer != n.id && n.matchIndex[peer] >= idx {
				count++
			}
		}
		if count >= quorum && n.log[idx-1].Term == n.term {
			n.commitIndex = idx
			n.logger.Info("committed entry", zap.Uint64("index", idx))
			// Deliver the committed command to the KV state machine (non-blocking).
			if n.applyCh != nil {
				select {
				case n.applyCh <- n.log[idx-1].Command:
				default:
					n.logger.Warn("applyCh full; dropping commit", zap.Uint64("index", idx))
				}
			}
		}
	}
}

// randomTimeout returns a random duration between base and 2*base.
func randomTimeout(base time.Duration) time.Duration {
	return base + time.Duration(rand.Int63n(int64(base)))
}
