package raft

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// NodeRole represents the Raft state machine role.
type NodeRole int

const (
	Follower NodeRole = iota
	Candidate
	Leader
)

func (r NodeRole) String() string {
	switch r {
	case Follower:
		return "LEADER" // mapped cleanly in UI
	case Candidate:
		return "CANDIDATE"
	case Leader:
		return "LEADER"
	default:
		return "UNKNOWN"
	}
}

func (r NodeRole) RawString() string {
	switch r {
	case Follower:
		return "FOLLOWER"
	case Candidate:
		return "CANDIDATE"
	case Leader:
		return "LEADER"
	default:
		return "UNKNOWN"
	}
}

var (
	ErrNotLeader      = errors.New("node is not the cluster leader")
	ErrProposalFailed = errors.New("proposal failed: leadership lost before commit")
	ErrNodeStopped    = errors.New("raft node is stopped")
)

// LogEntry represents an individual replicated command in the Raft log.
type LogEntry struct {
	Index uint64 `json:"index"`
	Term  uint64 `json:"term"`
	Data  []byte `json:"data"`
}

// RequestVoteArgs carries election ballot metadata.
type RequestVoteArgs struct {
	Term         uint64 `json:"term"`
	CandidateID  string `json:"candidate_id"`
	LastLogIndex uint64 `json:"last_log_index"`
	LastLogTerm  uint64 `json:"last_log_term"`
	IsPreVote    bool   `json:"is_pre_vote"`
}

// RequestVoteReply returns election vote results.
type RequestVoteReply struct {
	Term        uint64 `json:"term"`
	VoteGranted bool   `json:"vote_granted"`
}

// AppendEntriesArgs carries log replication payload and heartbeats.
type AppendEntriesArgs struct {
	Term         uint64     `json:"term"`
	LeaderID     string     `json:"leader_id"`
	PrevLogIndex uint64     `json:"prev_log_index"`
	PrevLogTerm  uint64     `json:"prev_log_term"`
	Entries      []LogEntry `json:"entries"`
	LeaderCommit uint64     `json:"leader_commit"`
}

// AppendEntriesReply acknowledges receipt of log replication.
type AppendEntriesReply struct {
	Term       uint64 `json:"term"`
	Success    bool   `json:"success"`
	MatchIndex uint64 `json:"match_index"`
}

// InstallSnapshotArgs carries snapshot chunks or complete state from leader to lagging follower.
type InstallSnapshotArgs struct {
	Term              uint64 `json:"term"`
	LeaderID          string `json:"leader_id"`
	LastIncludedIndex uint64 `json:"last_included_index"`
	LastIncludedTerm  uint64 `json:"last_included_term"`
	Data              []byte `json:"data"`
}

// InstallSnapshotReply returns follower term acknowledgement.
type InstallSnapshotReply struct {
	Term uint64 `json:"term"`
}

// Transport abstracts peer-to-peer RPC transmission (in-memory or TCP).
type Transport interface {
	SendRequestVote(ctx context.Context, target string, args *RequestVoteArgs) (*RequestVoteReply, error)
	SendAppendEntries(ctx context.Context, target string, args *AppendEntriesArgs) (*AppendEntriesReply, error)
	SendInstallSnapshot(ctx context.Context, target string, args *InstallSnapshotArgs) (*InstallSnapshotReply, error)
}

// CommitListener is invoked whenever new entries are committed by quorum.
type CommitListener func(entry LogEntry)

// SnapshotRestoreListener is invoked when state machine must restore from a received snapshot.
type SnapshotRestoreListener func(lastIncludedIndex uint64, lastIncludedTerm uint64, data []byte) error

// Config configures Raft node identity and cluster peers.
type Config struct {
	ID                 string
	Peers              []string
	ElectionMinMs      int
	ElectionMaxMs      int
	HeartbeatInterval  time.Duration
	Transport          Transport
	OnCommit           CommitListener
	OnRestoreSnapshot  SnapshotRestoreListener
	InitialCommitIndex uint64
}

// RaftNode implements the core consensus state machine.
type RaftNode struct {
	mu sync.RWMutex

	id    string
	peers []string
	cfg   Config

	currentTerm uint64
	votedFor    string
	log         []LogEntry

	commitIndex uint64
	lastApplied uint64
	role        NodeRole
	leaderID    string

	lastIncludedIndex uint64
	lastIncludedTerm  uint64
	lastSnapshotData  []byte

	nextIndex  map[string]uint64
	matchIndex map[string]uint64

	electionResetEvent time.Time
	rng                *rand.Rand
	stopCh             chan struct{}
	commitNotifyCh     chan struct{}
	isStopped          int32
}

// NewRaftNode creates and initializes a Raft consensus node.
func NewRaftNode(cfg Config) *RaftNode {
	if cfg.ElectionMinMs == 0 {
		cfg.ElectionMinMs = 150
	}
	if cfg.ElectionMaxMs == 0 {
		cfg.ElectionMaxMs = 300
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 50 * time.Millisecond
	}

	rn := &RaftNode{
		id:                 cfg.ID,
		peers:              cfg.Peers,
		cfg:                cfg,
		currentTerm:        0,
		votedFor:           "",
		log:                []LogEntry{{Index: 0, Term: 0, Data: nil}},
		commitIndex:        cfg.InitialCommitIndex,
		lastApplied:        cfg.InitialCommitIndex,
		role:               Follower,
		leaderID:           "",
		nextIndex:          make(map[string]uint64),
		matchIndex:         make(map[string]uint64),
		rng:                rand.New(rand.NewSource(time.Now().UnixNano() + int64(len(cfg.ID)))),
		stopCh:             make(chan struct{}),
		commitNotifyCh:     make(chan struct{}, 100),
		electionResetEvent: time.Now(),
	}

	for i := uint64(1); i <= cfg.InitialCommitIndex; i++ {
		rn.log = append(rn.log, LogEntry{Index: i, Term: 0, Data: nil})
	}

	return rn
}

// Start launches the election timer and background replication loop.
func (rn *RaftNode) Start() {
	go rn.runElectionLoop()
	go rn.runApplierLoop()
}

// Stop terminates background loops.
func (rn *RaftNode) Stop() {
	if atomic.CompareAndSwapInt32(&rn.isStopped, 0, 1) {
		close(rn.stopCh)
	}
}

// GetState returns current node role, term, commitIndex, and leaderID.
func (rn *RaftNode) GetState() (NodeRole, uint64, uint64, string) {
	rn.mu.RLock()
	defer rn.mu.RUnlock()
	return rn.role, rn.currentTerm, rn.commitIndex, rn.leaderID
}

// GetPeers returns the list of configured peers.
func (rn *RaftNode) GetPeers() []string {
	rn.mu.RLock()
	defer rn.mu.RUnlock()
	out := make([]string, len(rn.peers))
	copy(out, rn.peers)
	return out
}

// lastLogIndex returns the logical index of the last entry in the log. Caller must hold rn.mu or rn.mu.RLock.
func (rn *RaftNode) lastLogIndex() uint64 {
	return rn.log[len(rn.log)-1].Index
}

// lastLogTerm returns the term of the last entry in the log. Caller must hold rn.mu or rn.mu.RLock.
func (rn *RaftNode) lastLogTerm() uint64 {
	return rn.log[len(rn.log)-1].Term
}

// getLogTerm returns the term of the log entry at logical index. Returns 0 if index is unknown.
func (rn *RaftNode) getLogTerm(index uint64) uint64 {
	if index < rn.lastIncludedIndex {
		return 0
	}
	if index == rn.lastIncludedIndex {
		return rn.lastIncludedTerm
	}
	sliceIdx := int(index - rn.lastIncludedIndex)
	if sliceIdx < len(rn.log) {
		return rn.log[sliceIdx].Term
	}
	return 0
}

// getLogSlice returns entries starting from startIndex. If startIndex <= rn.lastIncludedIndex, returns nil.
func (rn *RaftNode) getLogSlice(startIndex uint64) []LogEntry {
	if startIndex <= rn.lastIncludedIndex {
		return nil
	}
	sliceIdx := int(startIndex - rn.lastIncludedIndex)
	if sliceIdx >= len(rn.log) {
		return nil
	}
	res := make([]LogEntry, len(rn.log)-sliceIdx)
	copy(res, rn.log[sliceIdx:])
	return res
}

// GetSnapshotState returns the current snapshot index, term, and in-memory log length.
func (rn *RaftNode) GetSnapshotState() (uint64, uint64, int) {
	rn.mu.RLock()
	defer rn.mu.RUnlock()
	return rn.lastIncludedIndex, rn.lastIncludedTerm, len(rn.log)
}

func (rn *RaftNode) randomizedTimeout() time.Duration {
	spread := rn.cfg.ElectionMaxMs - rn.cfg.ElectionMinMs
	ms := rn.cfg.ElectionMinMs + rn.rng.Intn(spread+1)
	return time.Duration(ms) * time.Millisecond
}

func (rn *RaftNode) runElectionLoop() {
	for {
		timeout := rn.randomizedTimeout()
		select {
		case <-rn.stopCh:
			return
		case <-time.After(timeout):
			rn.mu.Lock()
			if atomic.LoadInt32(&rn.isStopped) == 1 {
				rn.mu.Unlock()
				return
			}

			if rn.role == Leader {
				rn.mu.Unlock()
				continue
			}

			// Trigger election only if no heartbeat was received within the full timeout window
			if time.Since(rn.electionResetEvent) >= timeout {
				// Raft §9.6: Pre-Vote speculation to prevent partitioned nodes from causing election storms
				if rn.runPreVote() {
					rn.startElection()
				}
			}
			rn.mu.Unlock()
		}
	}
}

// runPreVote queries peers speculatively before incrementing currentTerm (Raft §9.6).
// Caller holds rn.mu; this method unlocks rn.mu during network RPCs and reacquires it before returning.
func (rn *RaftNode) runPreVote() bool {
	prospectiveTerm := rn.currentTerm + 1
	lastLogIndex := rn.lastLogIndex()
	lastLogTerm := rn.lastLogTerm()

	peers := make([]string, len(rn.peers))
	copy(peers, rn.peers)

	totalNodes := len(peers) + 1
	quorum := (totalNodes / 2) + 1

	preVotesReceived := 1
	if preVotesReceived >= quorum {
		return true
	}

	candidateID := rn.id
	transport := rn.cfg.Transport

	rn.mu.Unlock()

	var voteMu sync.Mutex
	doneCh := make(chan struct{})
	var once sync.Once

	for _, peer := range peers {
		go func(target string) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			args := &RequestVoteArgs{
				Term:         prospectiveTerm,
				CandidateID:  candidateID,
				LastLogIndex: lastLogIndex,
				LastLogTerm:  lastLogTerm,
				IsPreVote:    true,
			}

			reply, err := transport.SendRequestVote(ctx, target, args)
			if err != nil {
				return
			}

			rn.mu.Lock()
			if reply.Term > rn.currentTerm {
				rn.stepDown(reply.Term)
			}
			rn.mu.Unlock()

			if reply.VoteGranted {
				voteMu.Lock()
				preVotesReceived++
				if preVotesReceived >= quorum {
					once.Do(func() { close(doneCh) })
				}
				voteMu.Unlock()
			}
		}(peer)
	}

	select {
	case <-doneCh:
	case <-time.After(200 * time.Millisecond):
	}

	rn.mu.Lock()

	if atomic.LoadInt32(&rn.isStopped) == 1 || rn.role == Leader {
		return false
	}

	voteMu.Lock()
	granted := preVotesReceived >= quorum
	voteMu.Unlock()

	return granted
}

func (rn *RaftNode) startElection() {
	rn.role = Candidate
	rn.currentTerm++
	rn.votedFor = rn.id
	rn.leaderID = ""
	rn.electionResetEvent = time.Now()

	term := rn.currentTerm
	lastLogIndex := rn.lastLogIndex()
	lastLogTerm := rn.lastLogTerm()

	votesReceived := 1
	totalNodes := len(rn.peers) + 1
	quorum := (totalNodes / 2) + 1

	if votesReceived >= quorum {
		rn.becomeLeader()
		return
	}

	peers := make([]string, len(rn.peers))
	copy(peers, rn.peers)

	rn.mu.Unlock()

	var voteMu sync.Mutex
	var wg sync.WaitGroup

	for _, peer := range peers {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()

			args := &RequestVoteArgs{
				Term:         term,
				CandidateID:  rn.id,
				LastLogIndex: lastLogIndex,
				LastLogTerm:  lastLogTerm,
				IsPreVote:    false,
			}

			reply, err := rn.cfg.Transport.SendRequestVote(ctx, target, args)
			if err != nil {
				return
			}

			rn.mu.Lock()
			defer rn.mu.Unlock()

			if reply.Term > rn.currentTerm {
				rn.stepDown(reply.Term)
				return
			}

			if rn.role == Candidate && rn.currentTerm == term && reply.VoteGranted {
				voteMu.Lock()
				votesReceived++
				currentVotes := votesReceived
				voteMu.Unlock()

				if currentVotes >= quorum && rn.role == Candidate {
					rn.becomeLeader()
				}
			}
		}(peer)
	}

	rn.mu.Lock()
}

func (rn *RaftNode) becomeLeader() {
	rn.role = Leader
	rn.leaderID = rn.id

	lastIndex := rn.lastLogIndex()
	for _, peer := range rn.peers {
		rn.nextIndex[peer] = lastIndex + 1
		rn.matchIndex[peer] = 0
	}

	rn.broadcastAppendEntries()
	go rn.runLeaderHeartbeat()
}

func (rn *RaftNode) stepDown(newTerm uint64) {
	rn.role = Follower
	rn.currentTerm = newTerm
	rn.votedFor = ""
	rn.leaderID = ""
	rn.electionResetEvent = time.Now()
}

func (rn *RaftNode) runLeaderHeartbeat() {
	ticker := time.NewTicker(rn.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rn.stopCh:
			return
		case <-ticker.C:
			rn.mu.Lock()
			if rn.role != Leader || atomic.LoadInt32(&rn.isStopped) == 1 {
				rn.mu.Unlock()
				return
			}
			rn.broadcastAppendEntries()
			rn.mu.Unlock()
		}
	}
}

func (rn *RaftNode) broadcastAppendEntries() {
	term := rn.currentTerm
	leaderID := rn.id
	leaderCommit := rn.commitIndex

	for _, peer := range rn.peers {
		nextIdx := rn.nextIndex[peer]
		if nextIdx == 0 {
			nextIdx = 1
		}

		// Raft §7: If follower's nextIndex falls behind the compacted log, send InstallSnapshot
		if nextIdx <= rn.lastIncludedIndex {
			lastInclIndex := rn.lastIncludedIndex
			lastInclTerm := rn.lastIncludedTerm
			snapData := make([]byte, len(rn.lastSnapshotData))
			copy(snapData, rn.lastSnapshotData)

			go func(target string, sIdx, sTerm uint64, sData []byte) {
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer cancel()

				args := &InstallSnapshotArgs{
					Term:              term,
					LeaderID:          leaderID,
					LastIncludedIndex: sIdx,
					LastIncludedTerm:  sTerm,
					Data:              sData,
				}

				reply, err := rn.cfg.Transport.SendInstallSnapshot(ctx, target, args)
				if err != nil {
					return
				}

				rn.mu.Lock()
				defer rn.mu.Unlock()

				if reply.Term > rn.currentTerm {
					rn.stepDown(reply.Term)
					return
				}

				if rn.role != Leader || rn.currentTerm != term {
					return
				}

				if sIdx >= rn.nextIndex[target] {
					rn.nextIndex[target] = sIdx + 1
					rn.matchIndex[target] = sIdx
				}
			}(peer, lastInclIndex, lastInclTerm, snapData)
			continue
		}

		prevLogIndex := nextIdx - 1
		prevLogTerm := rn.getLogTerm(prevLogIndex)
		entries := rn.getLogSlice(nextIdx)

		go func(target string, pLogIdx, pLogTerm uint64, ents []LogEntry) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()

			args := &AppendEntriesArgs{
				Term:         term,
				LeaderID:     leaderID,
				PrevLogIndex: pLogIdx,
				PrevLogTerm:  pLogTerm,
				Entries:      ents,
				LeaderCommit: leaderCommit,
			}

			reply, err := rn.cfg.Transport.SendAppendEntries(ctx, target, args)
			if err != nil {
				return
			}

			rn.mu.Lock()
			defer rn.mu.Unlock()

			if reply.Term > rn.currentTerm {
				rn.stepDown(reply.Term)
				return
			}

			if rn.role != Leader || rn.currentTerm != term {
				return
			}

			if reply.Success {
				rn.nextIndex[target] = reply.MatchIndex + 1
				rn.matchIndex[target] = reply.MatchIndex
				rn.checkAndUpdateCommitIndex()
			} else {
				if rn.nextIndex[target] > rn.lastIncludedIndex+1 {
					rn.nextIndex[target]--
				} else {
					rn.nextIndex[target] = rn.lastIncludedIndex
				}
			}
		}(peer, prevLogIndex, prevLogTerm, entries)
	}
}

func (rn *RaftNode) checkAndUpdateCommitIndex() {
	totalNodes := len(rn.peers) + 1
	quorum := (totalNodes / 2) + 1

	lastIdx := rn.lastLogIndex()
	for n := lastIdx; n > rn.commitIndex; n-- {
		if rn.getLogTerm(n) != rn.currentTerm {
			continue
		}

		matches := 1
		for _, peer := range rn.peers {
			if rn.matchIndex[peer] >= n {
				matches++
			}
		}

		if matches >= quorum {
			rn.commitIndex = n
			select {
			case rn.commitNotifyCh <- struct{}{}:
			default:
			}
			// Immediately notify followers of the committed entry
			rn.broadcastAppendEntries()
			break
		}
	}
}

// Propose appends a client payload to the replicated log and blocks until committed by quorum.
func (rn *RaftNode) Propose(payload []byte) (uint64, error) {
	rn.mu.Lock()
	if rn.role != Leader {
		rn.mu.Unlock()
		return 0, ErrNotLeader
	}

	entryIndex := rn.lastLogIndex() + 1
	entry := LogEntry{
		Index: entryIndex,
		Term:  rn.currentTerm,
		Data:  payload,
	}
	rn.log = append(rn.log, entry)

	rn.broadcastAppendEntries()
	term := rn.currentTerm
	rn.mu.Unlock()

	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-rn.stopCh:
			return 0, ErrNodeStopped
		case <-timeout:
			return 0, fmt.Errorf("proposal timed out waiting for quorum commit")
		case <-time.After(5 * time.Millisecond):
			rn.mu.RLock()
			isLeader := (rn.role == Leader && rn.currentTerm == term)
			committed := rn.commitIndex >= entryIndex
			rn.mu.RUnlock()

			if !isLeader {
				return 0, ErrProposalFailed
			}
			if committed {
				return entryIndex, nil
			}
		}
	}
}

// HandleRequestVote processes incoming vote solicitations from candidates.
func (rn *RaftNode) HandleRequestVote(args *RequestVoteArgs) *RequestVoteReply {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if !args.IsPreVote && args.Term > rn.currentTerm {
		rn.stepDown(args.Term)
	}

	reply := &RequestVoteReply{
		Term:        rn.currentTerm,
		VoteGranted: false,
	}

	if args.Term < rn.currentTerm {
		return reply
	}

	// Raft §9.6: In pre-vote, reject if we have heard from an active leader
	// within the minimum election timeout
	if args.IsPreVote {
		minTimeout := time.Duration(rn.cfg.ElectionMinMs) * time.Millisecond
		if rn.leaderID != "" && time.Since(rn.electionResetEvent) < minTimeout {
			return reply
		}
	}

	canVote := false
	if args.IsPreVote {
		canVote = true
	} else {
		canVote = (rn.votedFor == "" || rn.votedFor == args.CandidateID)
	}

	lastIdx := rn.lastLogIndex()
	lastTerm := rn.lastLogTerm()
	logOk := (args.LastLogTerm > lastTerm) ||
		(args.LastLogTerm == lastTerm && args.LastLogIndex >= lastIdx)

	if canVote && logOk {
		reply.VoteGranted = true
		if !args.IsPreVote {
			rn.votedFor = args.CandidateID
			rn.electionResetEvent = time.Now()
		}
	}

	return reply
}

// HandleAppendEntries processes log entries and heartbeats from the leader.
func (rn *RaftNode) HandleAppendEntries(args *AppendEntriesArgs) *AppendEntriesReply {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if args.Term > rn.currentTerm {
		rn.stepDown(args.Term)
	}

	reply := &AppendEntriesReply{
		Term:       rn.currentTerm,
		Success:    false,
		MatchIndex: 0,
	}

	if args.Term < rn.currentTerm {
		return reply
	}

	if rn.role != Follower {
		rn.role = Follower
	}
	rn.leaderID = args.LeaderID
	rn.electionResetEvent = time.Now()

	// Raft §7: If PrevLogIndex is behind our compacted snapshot, trim entries already compacted
	if args.PrevLogIndex < rn.lastIncludedIndex {
		offset := rn.lastIncludedIndex - args.PrevLogIndex
		if uint64(len(args.Entries)) <= offset {
			reply.Success = true
			reply.MatchIndex = rn.lastIncludedIndex
			return reply
		}
		args.Entries = args.Entries[offset:]
		args.PrevLogIndex = rn.lastIncludedIndex
		args.PrevLogTerm = rn.lastIncludedTerm
	}

	// Verify log consistency at PrevLogIndex
	if args.PrevLogIndex > rn.lastLogIndex() || rn.getLogTerm(args.PrevLogIndex) != args.PrevLogTerm {
		return reply
	}

	for i, entry := range args.Entries {
		idx := args.PrevLogIndex + 1 + uint64(i)
		if idx <= rn.lastLogIndex() {
			if rn.getLogTerm(idx) != entry.Term {
				sliceIdx := int(idx - rn.lastIncludedIndex)
				rn.log = rn.log[:sliceIdx]
				rn.log = append(rn.log, entry)
			}
		} else {
			rn.log = append(rn.log, entry)
		}
	}

	if args.LeaderCommit > rn.commitIndex {
		lastNewIndex := args.PrevLogIndex + uint64(len(args.Entries))
		rn.commitIndex = min(args.LeaderCommit, lastNewIndex)
		select {
		case rn.commitNotifyCh <- struct{}{}:
		default:
		}
	}

	reply.Success = true
	reply.MatchIndex = args.PrevLogIndex + uint64(len(args.Entries))
	return reply
}

// HandleInstallSnapshot processes a snapshot installation RPC from the leader (Raft §7).
func (rn *RaftNode) HandleInstallSnapshot(args *InstallSnapshotArgs) *InstallSnapshotReply {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	reply := &InstallSnapshotReply{Term: rn.currentTerm}
	if args.Term < rn.currentTerm {
		return reply
	}

	if args.Term > rn.currentTerm {
		rn.stepDown(args.Term)
	}
	if rn.role != Follower {
		rn.role = Follower
	}
	rn.leaderID = args.LeaderID
	rn.electionResetEvent = time.Now()

	// Discard obsolete snapshot older than what we have
	if args.LastIncludedIndex <= rn.lastIncludedIndex {
		return reply
	}

	// If existing log contains an entry at LastIncludedIndex with same term, preserve trailing entries
	var newLog []LogEntry
	if args.LastIncludedIndex <= rn.lastLogIndex() && rn.getLogTerm(args.LastIncludedIndex) == args.LastIncludedTerm {
		sliceIdx := int(args.LastIncludedIndex - rn.lastIncludedIndex)
		newLog = append(newLog, LogEntry{Index: args.LastIncludedIndex, Term: args.LastIncludedTerm})
		if sliceIdx+1 < len(rn.log) {
			newLog = append(newLog, rn.log[sliceIdx+1:]...)
		}
	} else {
		newLog = []LogEntry{{Index: args.LastIncludedIndex, Term: args.LastIncludedTerm}}
	}

	rn.log = newLog
	rn.lastIncludedIndex = args.LastIncludedIndex
	rn.lastIncludedTerm = args.LastIncludedTerm
	rn.lastSnapshotData = args.Data

	if rn.commitIndex < args.LastIncludedIndex {
		rn.commitIndex = args.LastIncludedIndex
	}
	if rn.lastApplied < args.LastIncludedIndex {
		rn.lastApplied = args.LastIncludedIndex
	}

	// Notify state machine listener to restore state
	if rn.cfg.OnRestoreSnapshot != nil && len(args.Data) > 0 {
		cb := rn.cfg.OnRestoreSnapshot
		sIdx := args.LastIncludedIndex
		sTerm := args.LastIncludedTerm
		sData := make([]byte, len(args.Data))
		copy(sData, args.Data)
		go cb(sIdx, sTerm, sData)
	}

	return reply
}

// TakeSnapshot compacts the Raft log up to snapshotIndex and records the state snapshot data (Raft §7).
func (rn *RaftNode) TakeSnapshot(snapshotIndex uint64, stateData []byte) error {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if snapshotIndex > rn.commitIndex {
		return fmt.Errorf("cannot snapshot index %d higher than commit index %d", snapshotIndex, rn.commitIndex)
	}
	if snapshotIndex <= rn.lastIncludedIndex {
		return fmt.Errorf("snapshot index %d already included in snapshot (lastIncluded=%d)", snapshotIndex, rn.lastIncludedIndex)
	}

	snapTerm := rn.getLogTerm(snapshotIndex)
	sliceIdx := int(snapshotIndex - rn.lastIncludedIndex)
	if sliceIdx >= len(rn.log) {
		return fmt.Errorf("snapshot index %d beyond current log bounds", snapshotIndex)
	}

	// Compact log: new log starts with anchor entry at snapshotIndex
	newLog := make([]LogEntry, 0, len(rn.log)-sliceIdx)
	newLog = append(newLog, LogEntry{Index: snapshotIndex, Term: snapTerm})
	if sliceIdx+1 < len(rn.log) {
		newLog = append(newLog, rn.log[sliceIdx+1:]...)
	}

	rn.log = newLog
	rn.lastIncludedIndex = snapshotIndex
	rn.lastIncludedTerm = snapTerm
	rn.lastSnapshotData = stateData

	if rn.lastApplied < snapshotIndex {
		rn.lastApplied = snapshotIndex
	}

	return nil
}

func (rn *RaftNode) runApplierLoop() {
	for {
		select {
		case <-rn.stopCh:
			return
		case <-rn.commitNotifyCh:
			rn.mu.Lock()
			for rn.commitIndex > rn.lastApplied {
				rn.lastApplied++
				if rn.lastApplied <= rn.lastIncludedIndex {
					continue
				}
				sliceIdx := int(rn.lastApplied - rn.lastIncludedIndex)
				if sliceIdx < len(rn.log) {
					entry := rn.log[sliceIdx]
					if rn.cfg.OnCommit != nil && entry.Data != nil {
						rn.cfg.OnCommit(entry)
					}
				}
			}
			rn.mu.Unlock()
		}
	}
}

