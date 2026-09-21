package tests

import (
	"fmt"
	"testing"
	"time"

	"KhorosLog/raft"
	"KhorosLog/storage"
)

func TestChaos_LinearizabilityUnderLeaderFailure(t *testing.T) {
	tempBase := t.TempDir()
	net := newMockNetwork()
	nodeIDs := []string{"node-1", "node-2", "node-3"}

	type clusterNode struct {
		rn  *raft.RaftNode
		wal *storage.WAL
	}
	cluster := make(map[string]*clusterNode)

	for _, id := range nodeIDs {
		var peers []string
		for _, p := range nodeIDs {
			if p != id {
				peers = append(peers, p)
			}
		}

		nodeDir := fmt.Sprintf("%s/%s", tempBase, id)
		wal, err := storage.OpenWAL(storage.Config{
			DataDir:            nodeDir,
			MaxSegmentBytes:    64 * 1024,
			IndexIntervalBytes: 512,
			SyncOnWrite:        true,
		})
		if err != nil {
			t.Fatalf("OpenWAL failed for %s: %v", id, err)
		}

		cfg := raft.Config{
			ID:                id,
			Peers:             peers,
			ElectionMinMs:     100,
			ElectionMaxMs:     200,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         &raftMockTransport{senderID: id, net: net},
			OnCommit: func(entry raft.LogEntry) {
				_, _ = wal.Append(entry.Data)
			},
		}

		rn := raft.NewRaftNode(cfg)
		cluster[id] = &clusterNode{rn: rn, wal: wal}
		net.Register(id, rn)
		rn.Start()
	}

	defer func() {
		for _, n := range cluster {
			n.rn.Stop()
			_ = n.wal.Close()
		}
	}()

	// Allow initial election to settle
	time.Sleep(350 * time.Millisecond)

	var leaderID string
	var leaderNode *clusterNode
	for id, n := range cluster {
		role, _, _, _ := n.rn.GetState()
		if role == raft.Leader {
			leaderID = id
			leaderNode = n
			break
		}
	}

	if leaderNode == nil {
		t.Fatalf("no initial leader elected")
	}

	// 1. Propose 10 writes to initial leader
	committedCount := 0
	for i := 1; i <= 10; i++ {
		_, err := leaderNode.rn.Propose([]byte(fmt.Sprintf("chaos-event-%03d", i)))
		if err != nil {
			t.Fatalf("write failed prior to chaos: %v", err)
		}
		committedCount++
	}

	// 2. CHAOS INJECTION: Abruptly isolate/kill current leader
	t.Logf("Injecting chaos: killing leader %s", leaderID)
	leaderNode.rn.Stop()
	for _, p := range nodeIDs {
		if p != leaderID {
			net.Disconnect(leaderID, p)
		}
	}

	// 3. Quorum failover: Remaining 2 nodes elect new leader
	time.Sleep(450 * time.Millisecond)

	var newLeaderID string
	var newLeaderNode *clusterNode
	for id, n := range cluster {
		if id == leaderID {
			continue
		}
		role, _, _, _ := n.rn.GetState()
		if role == raft.Leader {
			newLeaderID = id
			newLeaderNode = n
			break
		}
	}

	if newLeaderNode == nil {
		t.Fatalf("failover failed: no new leader elected after chaos kill")
	}
	t.Logf("Quorum failover successful: new leader %s elected", newLeaderID)

	// 4. Propose 10 more writes to new leader
	for i := 11; i <= 20; i++ {
		_, err := newLeaderNode.rn.Propose([]byte(fmt.Sprintf("chaos-event-%03d", i)))
		if err != nil {
			t.Fatalf("write failed on new leader: %v", err)
		}
		committedCount++
	}

	time.Sleep(100 * time.Millisecond)

	// 5. LINEARIZABILITY VERIFICATION: Verify new leader WAL has all committed records without loss
	for i := 0; i < 20; i++ {
		data, err := newLeaderNode.wal.ReadAt(uint64(i))
		if err != nil {
			t.Fatalf("linearizability violation: committed record at offset %d lost after failover: %v", i, err)
		}
		expected := fmt.Sprintf("chaos-event-%03d", i+1)
		if string(data) != expected {
			t.Fatalf("offset %d corrupted: got %s, want %s", i, string(data), expected)
		}
	}

	t.Log("Linearizability under chaos fully verified: 20/20 sequential writes preserved across leader termination.")
}
