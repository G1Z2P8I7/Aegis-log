//go:build ignore

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"KhorosLog/raft"
)

type failoverNet struct {
	mu           sync.RWMutex
	nodes        map[string]*raft.RaftNode
	disconnected map[string]bool
}

type failoverTransport struct {
	net      *failoverNet
	senderID string
}

func (ft *failoverTransport) SendRequestVote(ctx context.Context, target string, args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	ft.net.mu.RLock()
	if ft.net.disconnected[target] || ft.net.disconnected[ft.senderID] {
		ft.net.mu.RUnlock()
		return nil, fmt.Errorf("target partitioned")
	}
	targetNode := ft.net.nodes[target]
	ft.net.mu.RUnlock()

	if targetNode == nil {
		return nil, fmt.Errorf("target node not found")
	}
	return targetNode.HandleRequestVote(args), nil
}

func (ft *failoverTransport) SendAppendEntries(ctx context.Context, target string, args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	ft.net.mu.RLock()
	if ft.net.disconnected[target] || ft.net.disconnected[ft.senderID] {
		ft.net.mu.RUnlock()
		return nil, fmt.Errorf("target partitioned")
	}
	targetNode := ft.net.nodes[target]
	ft.net.mu.RUnlock()

	if targetNode == nil {
		return nil, fmt.Errorf("target node not found")
	}
	return targetNode.HandleAppendEntries(args), nil
}

func main() {
	fmt.Println("================================================================")
	fmt.Println("       KhorosLog — Leader Failover Recovery Time Benchmark      ")
	fmt.Println("================================================================")

	net := &failoverNet{
		nodes:        make(map[string]*raft.RaftNode),
		disconnected: make(map[string]bool),
	}
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
			ElectionMinMs:     60, // Optimized fast failure detection
			ElectionMaxMs:     120,
			HeartbeatInterval: 20 * time.Millisecond,
			Transport:         &failoverTransport{net: net, senderID: id},
		})
		nodes[id] = rn
		net.nodes[id] = rn
		rn.Start()
	}

	defer func() {
		for _, rn := range nodes {
			rn.Stop()
		}
	}()

	time.Sleep(300 * time.Millisecond)

	var initialLeaderID string
	var initialLeader *raft.RaftNode
	for id, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			initialLeaderID = id
			initialLeader = rn
			break
		}
	}

	if initialLeader == nil {
		panic("no initial leader elected")
	}
	fmt.Printf("Initial Leader Established: %s\n", initialLeaderID)

	// Verify initial write
	_, err := initialLeader.Propose([]byte("pre-failover-event"))
	if err != nil {
		panic(err)
	}

	fmt.Println("Injecting chaos: killing active leader immediately...")

	killTime := time.Now()
	// Abruptly terminate initial leader and isolate from network
	initialLeader.Stop()
	net.mu.Lock()
	net.disconnected[initialLeaderID] = true
	net.mu.Unlock()

	// Poll remaining 2 nodes until a new leader is elected and accepts a write
	var recoveryElapsed time.Duration
	var newLeaderID string
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		for id, rn := range nodes {
			if id == initialLeaderID {
				continue
			}
			role, _, _, _ := rn.GetState()
			if role == raft.Leader {
				// Attempt client write
				if _, err := rn.Propose([]byte("post-failover-event")); err == nil {
					recoveryElapsed = time.Since(killTime)
					newLeaderID = id
					break
				}
			}
		}
		if newLeaderID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if newLeaderID == "" {
		panic("failover timed out after 3 seconds")
	}

	fmt.Println("\n[Leader Failover Metrics]")
	fmt.Printf("  • Terminated Leader: %s\n", initialLeaderID)
	fmt.Printf("  • New Elected Leader: %s\n", newLeaderID)
	fmt.Printf("  • Total Recovery Time: %.2f ms\n", float64(recoveryElapsed.Microseconds())/1000.0)

	if recoveryElapsed < 150*time.Millisecond {
		fmt.Printf("  • Status: PASS (< 150ms SLA Target Achieved!)\n")
	} else {
		fmt.Printf("  • Status: NOTICE (Recovered in %.2f ms)\n", float64(recoveryElapsed.Microseconds())/1000.0)
	}
	fmt.Println("================================================================")
}
