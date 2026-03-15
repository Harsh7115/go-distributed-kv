package raft

import (
	"encoding/json"
	"net/http"
)

// ---------------------------------------------------------------------------
// Server-side Raft RPC handlers
//
// HandleAppendEntries and HandleRequestVote are the receiving end of the two
// core Raft RPCs. They are called by RaftHandler (below) which decodes the
// JSON body and encodes the reply. Both methods safely acquire n.mu.
// ---------------------------------------------------------------------------

// HandleAppendEntries processes an incoming AppendEntries RPC (§5.3, §5.4).
// It is safe to call concurrently.
func (n *Node) HandleAppendEntries(args *AppendEntriesArgs) *AppendEntriesReply {
	n.mu.Lock()
	defer n.mu.Unlock()

	reply := &AppendEntriesReply{}

	// §5.1 rule 1: reject stale leaders.
	if args.Term < n.term {
		reply.Term = n.term
		return reply
	}

	// A higher term always supersedes our current state — step down.
	if args.Term > n.term {
		n.term = args.Term
		n.votedFor = 0
		n.state.Store(int32(Follower))
	}

	// Valid leader contact: reset election timer and ensure follower state.
	n.electionElapsed = 0
	n.state.Store(int32(Follower))
	reply.Term = n.term

	// §5.3 rule 2: prevLogIndex consistency check.
	if args.PrevLogIndex > 0 {
		if args.PrevLogIndex > n.lastLogIndex() {
			// We are missing entries — tell leader where to retry.
			reply.ConflictIndex = n.lastLogIndex() + 1
			reply.ConflictTerm = 0
			return reply
		}
		if n.log[args.PrevLogIndex-1].Term != args.PrevLogTerm {
			// Conflict — report conflicting term for fast log back-tracking.
			reply.ConflictTerm = n.log[args.PrevLogIndex-1].Term
			idx := args.PrevLogIndex
			for idx > 1 && n.log[idx-2].Term == reply.ConflictTerm {
				idx--
			}
			reply.ConflictIndex = idx
			return reply
		}
	}

	// §5.3 rules 3 & 4: append new entries, truncating any conflicting suffix.
	for i, entry := range args.Entries {
		logIdx := args.PrevLogIndex + uint64(i) + 1
		if logIdx <= n.lastLogIndex() {
			if n.log[logIdx-1].Term != entry.Term {
				n.log = n.log[:logIdx-1] // truncate conflicting suffix
			} else {
				continue // already stored, skip
			}
		}
		n.log = append(n.log, entry)
	}

	// §5.3 rule 5: advance commitIndex to min(leaderCommit, last new entry).
	if args.LeaderCommit > n.commitIndex {
		newCommit := args.LeaderCommit
		if last := n.lastLogIndex(); last < newCommit {
			newCommit = last
		}
		if newCommit > n.commitIndex {
			n.commitIndex = newCommit
			n.applyCommitted()
		}
	}

	reply.Success = true
	return reply
}

// HandleRequestVote processes an incoming RequestVote RPC (§5.2, §5.4).
// It is safe to call concurrently.
func (n *Node) HandleRequestVote(args *RequestVoteArgs) *RequestVoteReply {
	n.mu.Lock()
	defer n.mu.Unlock()

	reply := &RequestVoteReply{}

	// §5.1 rule 1: reject stale candidates.
	if args.Term < n.term {
		reply.Term = n.term
		return reply
	}

	// Step down on a higher term.
	if args.Term > n.term {
		n.term = args.Term
		n.votedFor = 0
		n.state.Store(int32(Follower))
	}

	reply.Term = n.term

	// §5.2 vote grant conditions:
	//   (a) we have not yet voted in this term, or already voted for this candidate
	//   (b) candidate's log is at least as up-to-date as ours (§5.4.1)
	canVote := n.votedFor == 0 || n.votedFor == args.CandidateID
	candidateUpToDate := args.LastLogTerm > n.lastLogTerm() ||
		(args.LastLogTerm == n.lastLogTerm() && args.LastLogIndex >= n.lastLogIndex())

	if canVote && candidateUpToDate {
		n.votedFor = args.CandidateID
		n.electionElapsed = 0 // reset timer when granting a vote
		reply.VoteGranted = true
		n.logger.Sugar().Infow("vote granted",
			"to_candidate", args.CandidateID,
			"term", args.Term,
		)
	}

	return reply
}

// applyCommitted delivers entries in (lastApplied, commitIndex] to applyCh.
// Caller must hold n.mu.
func (n *Node) applyCommitted() {
	for n.lastApplied < n.commitIndex {
		n.lastApplied++
		if n.applyCh != nil {
			select {
			case n.applyCh <- n.log[n.lastApplied-1].Command:
			default:
				n.logger.Sugar().Warnw("applyCh full; dropping apply",
					"index", n.lastApplied,
				)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// RaftHandler — HTTP server-side of the Raft transport
//
// Mount this on the Raft listen address (--raft flag), not the KV API port,
// so that these internal RPC endpoints are never reachable by external clients.
// ---------------------------------------------------------------------------

// RaftHandler serves Raft RPC endpoints over HTTP/1.1 + JSON.
type RaftHandler struct {
	node *Node
}

// NewRaftHandler creates an RaftHandler backed by node.
func NewRaftHandler(node *Node) *RaftHandler {
	return &RaftHandler{node: node}
}

// Register mounts the two Raft RPC endpoints on mux:
//
//	POST /raft/append-entries   — AppendEntries (leader → followers)
//	POST /raft/request-vote     — RequestVote   (candidate → peers)
func (h *RaftHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/raft/append-entries", h.handleAppendEntries)
	mux.HandleFunc("/raft/request-vote", h.handleRequestVote)
}

func (h *RaftHandler) handleAppendEntries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var args AppendEntriesArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	reply := h.node.HandleAppendEntries(&args)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply) //nolint:errcheck
}

func (h *RaftHandler) handleRequestVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var args RequestVoteArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	reply := h.node.HandleRequestVote(&args)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply) //nolint:errcheck
}
