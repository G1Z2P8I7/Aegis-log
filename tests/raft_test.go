package tests

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"KhorosLog/raft"
)

type mockNetwork struct {
	mu           sync.RWMutex
	nodes        map[string]*raft.RaftNode
	disconnected map[string]bool
}

func newMockNetwork() *mockNetwork {
	return &mockNetwork{
		nodes:        make(map[string]*raft.RaftNode),
		disconnected: make(map[string]bool),
	}
}

func (m *mockNetwork) Register(id string, rn *raft.RaftNode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[id] = rn
}

func (m *mockNetwork) Disconnect(from, to string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnected[fmt.Sprintf("%s->%s", from, to)] = true
	m.disconnected[fmt.Sprintf("%s->%s", to, from)] = true
}

func (m *mockNetwork) Reconnect(from, to string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.disconnected, fmt.Sprintf("%s->%s", from, to))
	delete(m.disconnected, fmt.Sprintf("%s->%s", to, from))
}

type raftMockTransport struct {
	senderID string
	net      *mockNetwork
}

func (rt *raftMockTransport) SendRequestVote(ctx context.Context, target string, args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	rt.net.mu.RLock()
	if rt.net.disconnected[fmt.Sprintf("%s->%s", rt.senderID, target)] {
		rt.net.mu.RUnlock()
		return nil, fmt.Errorf("network link partitioned")
	}
	targetNode, exists := rt.net.nodes[target]
	rt.net.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("node not found")
	}
	return targetNode.HandleRequestVote(args), nil
}

func (rt *raftMockTransport) SendAppendEntries(ctx context.Context, target string, args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	rt.net.mu.RLock()
	if rt.net.disconnected[fmt.Sprintf("%s->%s", rt.senderID, target)] {
		rt.net.mu.RUnlock()
		return nil, fmt.Errorf("network link partitioned")
	}
	targetNode, exists := rt.net.nodes[target]
	rt.net.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("node not found")
	}
	return targetNode.HandleAppendEntries(args), nil
}

func (rt *raftMockTransport) SendInstallSnapshot(ctx context.Context, target string, args *raft.InstallSnapshotArgs) (*raft.InstallSnapshotReply, error) {
	rt.net.mu.RLock()
	if rt.net.disconnected[fmt.Sprintf("%s->%s", rt.senderID, target)] {
		rt.net.mu.RUnlock()
		return nil, fmt.Errorf("network link partitioned")
	}
	targetNode, exists := rt.net.nodes[target]
	rt.net.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("node not found")
	}
	return targetNode.HandleInstallSnapshot(args), nil
}

func TestRaft_LeaderElection(t *testing.T) {
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	nodes := make([]*raft.RaftNode, 3)

	for i, id := range nodeIDs {
		var peers []string
		for _, peer := range nodeIDs {
			if peer != id {
				peers = append(peers, peer)
			}
		}

		cfg := raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     100,
			ElectionMaxMs:     200,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
		}

		rn := raft.NewRaftNode(cfg)
		nodes[i] = rn
		net.Register(id, rn)
	}

	for _, rn := range nodes {
		rn.Start()
		defer rn.Stop()
	}

	time.Sleep(350 * time.Millisecond)

	leaderCount := 0
	var leaderID string
	for _, rn := range nodes {
		role, term, _, lid := rn.GetState()
		if role == raft.Leader {
			leaderCount++
			leaderID = lid
			if term == 0 {
				t.Errorf("leader has term 0")
			}
		}
	}

	if leaderCount != 1 {
		t.Fatalf("expected exactly 1 leader, got %d", leaderCount)
	}
	t.Logf("Successfully elected leader: %s", leaderID)
}

func TestRaft_ReplicationAndLinearizability(t *testing.T) {
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	nodes := make(map[string]*raft.RaftNode)

	for _, id := range nodeIDs {
		var peers []string
		for _, p := range nodeIDs {
			if p != id {
				peers = append(peers, p)
			}
		}
		rn := raft.NewRaftNode(raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     100,
			ElectionMaxMs:     200,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
		})
		nodes[id] = rn
		net.Register(id, rn)
		rn.Start()
		defer rn.Stop()
	}

	time.Sleep(350 * time.Millisecond)

	var leader *raft.RaftNode
	for _, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			leader = rn
			break
		}
	}

	if leader == nil {
		t.Fatalf("no leader found after election")
	}

	for i := 1; i <= 5; i++ {
		payload := []byte(fmt.Sprintf("replicated-event-%d", i))
		idx, err := leader.Propose(payload)
		if err != nil {
			t.Fatalf("Propose failed at %d: %v", i, err)
		}
		if idx != uint64(i) {
			t.Fatalf("expected commit index %d, got %d", i, idx)
		}
	}

	time.Sleep(100 * time.Millisecond)
	for id, rn := range nodes {
		_, _, commitIdx, _ := rn.GetState()
		if commitIdx < 5 {
			t.Errorf("node %s has commit index %d, expected >= 5", id, commitIdx)
		}
	}
}

func TestRaft_NetworkPartitionFailover(t *testing.T) {
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	nodes := make(map[string]*raft.RaftNode)

	for _, id := range nodeIDs {
		var peers []string
		for _, p := range nodeIDs {
			if p != id {
				peers = append(peers, p)
			}
		}
		rn := raft.NewRaftNode(raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     100,
			ElectionMaxMs:     200,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
		})
		nodes[id] = rn
		net.Register(id, rn)
		rn.Start()
		defer rn.Stop()
	}

	time.Sleep(350 * time.Millisecond)

	var oldLeaderID string
	for id, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			oldLeaderID = id
			break
		}
	}

	if oldLeaderID == "" {
		t.Fatalf("no initial leader elected")
	}

	for _, p := range nodeIDs {
		if p != oldLeaderID {
			net.Disconnect(oldLeaderID, p)
		}
	}

	// Allow up to 1.5s for randomized election timers to resolve split votes and elect a new leader
	var newLeaderID string
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for id, rn := range nodes {
			if id == oldLeaderID {
				continue
			}
			role, _, _, _ := rn.GetState()
			if role == raft.Leader {
				newLeaderID = id
				break
			}
		}
		if newLeaderID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if newLeaderID == "" {
		t.Fatalf("quorum partition failed to elect a new leader within 1.5s")
	}
	if newLeaderID == oldLeaderID {
		t.Fatalf("new leader is the old isolated leader")
	}

	t.Logf("Failover successful: old leader %s isolated, new leader %s elected by quorum", oldLeaderID, newLeaderID)
}

func TestRaft_PreVotePartitionIsolation(t *testing.T) {
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	nodes := make(map[string]*raft.RaftNode)

	for _, id := range nodeIDs {
		var peers []string
		for _, p := range nodeIDs {
			if p != id {
				peers = append(peers, p)
			}
		}
		rn := raft.NewRaftNode(raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     80,
			ElectionMaxMs:     150,
			HeartbeatInterval: 25 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
		})
		nodes[id] = rn
		net.Register(id, rn)
		rn.Start()
		defer rn.Stop()
	}

	time.Sleep(300 * time.Millisecond)

	var leaderID string
	var followerID string
	for id, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			leaderID = id
		} else if followerID == "" {
			followerID = id
		}
	}

	if leaderID == "" || followerID == "" {
		t.Fatalf("failed to identify leader and follower")
	}

	_, initialLeaderTerm, _, _ := nodes[leaderID].GetState()

	// Partition the follower from all peers
	for _, p := range nodeIDs {
		if p != followerID {
			net.Disconnect(followerID, p)
		}
	}

	// Sleep across multiple election timeout intervals (500ms > 3x max election timeout)
	time.Sleep(500 * time.Millisecond)

	// Verify the partitioned follower's term did NOT escalate into double digits
	_, followerTerm, _, _ := nodes[followerID].GetState()
	if followerTerm > initialLeaderTerm+1 {
		t.Fatalf("election storm detected: partitioned follower term escalated to %d (expected <= %d)",
			followerTerm, initialLeaderTerm+1)
	}

	// Heal the partition
	for _, p := range nodeIDs {
		if p != followerID {
			net.Reconnect(followerID, p)
		}
	}

	time.Sleep(200 * time.Millisecond)

	// Quorum leader should remain healthy and term should not have skyrocketed
	role, currentLeaderTerm, _, _ := nodes[leaderID].GetState()
	if role == raft.Leader && currentLeaderTerm > initialLeaderTerm+2 {
		t.Fatalf("leader term escalated excessively after partition heal: %d", currentLeaderTerm)
	}
	t.Logf("Pre-Vote verification passed: partitioned follower term held at %d, cluster stable at term %d",
		followerTerm, currentLeaderTerm)
}

func TestRaft_SnapshotAndCompaction(t *testing.T) {
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	nodes := make(map[string]*raft.RaftNode)

	var restoredSnapshotData []byte
	var restoredMu sync.Mutex

	for _, id := range nodeIDs {
		var peers []string
		for _, peer := range nodeIDs {
			if peer != id {
				peers = append(peers, peer)
			}
		}

		cfg := raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     100,
			ElectionMaxMs:     200,
			HeartbeatInterval: 25 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
			OnRestoreSnapshot: func(lastIdx, lastTerm uint64, data []byte) error {
				restoredMu.Lock()
				restoredSnapshotData = data
				restoredMu.Unlock()
				return nil
			},
		}

		rn := raft.NewRaftNode(cfg)
		nodes[id] = rn
		net.Register(id, rn)
	}

	for _, rn := range nodes {
		rn.Start()
		defer rn.Stop()
	}

	// Wait for leader
	time.Sleep(350 * time.Millisecond)
	var leaderID string
	for id, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			leaderID = id
			break
		}
	}
	if leaderID == "" {
		t.Fatalf("no leader elected")
	}

	leader := nodes[leaderID]

	// Propose 8 entries
	for i := 1; i <= 8; i++ {
		idx, err := leader.Propose([]byte(fmt.Sprintf("msg-%d", i)))
		if err != nil {
			t.Fatalf("proposal %d failed: %v", i, err)
		}
		if idx != uint64(i) {
			t.Fatalf("expected entry index %d, got %d", i, idx)
		}
	}

	// Leader takes snapshot at index 5
	snapPayload := []byte("state-machine-checkpoint-at-5")
	if err := leader.TakeSnapshot(5, snapPayload); err != nil {
		t.Fatalf("leader TakeSnapshot failed: %v", err)
	}

	sIdx, sTerm, logLen := leader.GetSnapshotState()
	if sIdx != 5 || sTerm == 0 || logLen == 0 {
		t.Fatalf("invalid snapshot state on leader: idx=%d, term=%d, logLen=%d", sIdx, sTerm, logLen)
	}

	// Now pick a follower to partition so it falls behind the leader's snapshot
	var followerID string
	for id := range nodes {
		if id != leaderID {
			followerID = id
			break
		}
	}

	for _, id := range nodeIDs {
		if id != followerID {
			net.Disconnect(followerID, id)
		}
	}

	// Leader proposes entries 9 to 12 (committed by leader + other follower)
	for i := 9; i <= 12; i++ {
		_, err := leader.Propose([]byte(fmt.Sprintf("msg-%d", i)))
		if err != nil {
			t.Fatalf("proposal %d while node-3 partitioned failed: %v", i, err)
		}
	}

	// Leader compacts log further up to index 10
	snapPayload2 := []byte("state-machine-checkpoint-at-10")
	if err := leader.TakeSnapshot(10, snapPayload2); err != nil {
		t.Fatalf("leader TakeSnapshot at 10 failed: %v", err)
	}

	// Reconnect node-3
	// Leader's log only starts at 10 now, but node-3's nextIndex is <= 8.
	// Leader must detect nextIndex <= lastIncludedIndex and trigger InstallSnapshot!
	for _, id := range nodeIDs {
		if id != followerID {
			net.Reconnect(followerID, id)
		}
	}

	// Allow time for InstallSnapshot and subsequent Catch-up replication
	time.Sleep(300 * time.Millisecond)

	// Check that node-3 restored snapshot
	restoredMu.Lock()
	rData := string(restoredSnapshotData)
	restoredMu.Unlock()

	if rData != string(snapPayload2) {
		t.Fatalf("node-3 failed to restore snapshot: expected %q, got %q", string(snapPayload2), rData)
	}

	// Now propose another entry to confirm full cluster health
	idx, err := leader.Propose([]byte("cluster-healthy-after-snapshot"))
	if err != nil {
		t.Fatalf("proposal after catch-up failed: %v", err)
	}
	if idx != 13 {
		t.Fatalf("expected entry index 13, got %d", idx)
	}

	t.Logf("Snapshot and compaction test passed successfully!")
}
