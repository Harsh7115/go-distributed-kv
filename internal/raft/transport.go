package raft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// RPC argument / reply types
// ---------------------------------------------------------------------------

// AppendEntriesArgs is sent by the leader to replicate log entries and as a
// heartbeat when Entries is empty.
type AppendEntriesArgs struct {
	Term         uint64     `json:"term"`
	LeaderID     uint64     `json:"leader_id"`
	PrevLogIndex uint64     `json:"prev_log_index"`
	PrevLogTerm  uint64     `json:"prev_log_term"`
	Entries      []LogEntry `json:"entries"`
	LeaderCommit uint64     `json:"leader_commit"`
}

// AppendEntriesReply is the follower response to AppendEntries.
// ConflictIndex / ConflictTerm enable fast log back-tracking (§5.3).
type AppendEntriesReply struct {
	Term          uint64 `json:"term"`
	Success       bool   `json:"success"`
	ConflictIndex uint64 `json:"conflict_index"`
	ConflictTerm  uint64 `json:"conflict_term"`
}

// RequestVoteArgs is sent by candidates during an election.
type RequestVoteArgs struct {
	Term         uint64 `json:"term"`
	CandidateID  uint64 `json:"candidate_id"`
	LastLogIndex uint64 `json:"last_log_index"`
	LastLogTerm  uint64 `json:"last_log_term"`
}

// RequestVoteReply is the peer response to RequestVote.
type RequestVoteReply struct {
	Term        uint64 `json:"term"`
	VoteGranted bool   `json:"vote_granted"`
}

// ---------------------------------------------------------------------------
// Transport interface
// ---------------------------------------------------------------------------

// Transport abstracts peer-to-peer Raft RPC communication so the core
// algorithm in Node remains transport-agnostic (HTTP, gRPC, in-process …).
type Transport interface {
	// SendAppendEntries delivers an AppendEntries RPC to the given peer address
	// and returns the reply.  peerAddr is a host:port string.
	SendAppendEntries(peerAddr string, args *AppendEntriesArgs) (*AppendEntriesReply, error)

	// SendRequestVote delivers a RequestVote RPC to the given peer.
	SendRequestVote(peerAddr string, args *RequestVoteArgs) (*RequestVoteReply, error)

	// Close releases any resources held by the transport.
	Close() error
}

// ---------------------------------------------------------------------------
// HTTPTransport — a Transport backed by plain HTTP + JSON
// ---------------------------------------------------------------------------

// HTTPTransport implements Transport over HTTP/1.1 + JSON.  It is suitable
// for local development and testing; swap in a gRPC transport for production.
type HTTPTransport struct {
	client *http.Client
}

// NewHTTPTransport creates an HTTPTransport with a sensible default timeout.
func NewHTTPTransport(timeout time.Duration) *HTTPTransport {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &HTTPTransport{
		client: &http.Client{Timeout: timeout},
	}
}

func (t *HTTPTransport) post(url string, body interface{}, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	resp, err := t.client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode reply: %w", err)
	}
	return nil
}

// SendAppendEntries POSTs to /raft/append-entries on the remote peer.
func (t *HTTPTransport) SendAppendEntries(peerAddr string, args *AppendEntriesArgs) (*AppendEntriesReply, error) {
	var reply AppendEntriesReply
	url := fmt.Sprintf("http://%s/raft/append-entries", peerAddr)
	if err := t.post(url, args, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// SendRequestVote POSTs to /raft/request-vote on the remote peer.
func (t *HTTPTransport) SendRequestVote(peerAddr string, args *RequestVoteArgs) (*RequestVoteReply, error) {
	var reply RequestVoteReply
	url := fmt.Sprintf("http://%s/raft/request-vote", peerAddr)
	if err := t.post(url, args, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// Close drains idle connections held by the underlying HTTP client.
func (t *HTTPTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}
