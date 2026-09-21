//go:build ignore

package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"KhorosLog/raft"
)

type benchMockNet struct {
	mu    sync.RWMutex
	nodes map[string]*raft.RaftNode
}

type benchTransport struct {
	net      *benchMockNet
	senderID string
}

func (b *benchTransport) SendRequestVote(ctx context.Context, target string, args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	b.net.mu.RLock()
	targetNode := b.net.nodes[target]
	b.net.mu.RUnlock()
	return targetNode.HandleRequestVote(args), nil
}

func (b *benchTransport) SendAppendEntries(ctx context.Context, target string, args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	b.net.mu.RLock()
	targetNode := b.net.nodes[target]
	b.net.mu.RUnlock()
	return targetNode.HandleAppendEntries(args), nil
}

func main() {
	fmt.Println("================================================================")
	fmt.Println("    KhorosLog — End-to-End Raft Replication Latency Benchmark   ")
	fmt.Println("================================================================")

	net := &benchMockNet{nodes: make(map[string]*raft.RaftNode)}
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
			ElectionMinMs:     400,
			ElectionMaxMs:     800,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         &benchTransport{net: net, senderID: id},
		})
		nodes[id] = rn
		net.nodes[id] = rn
		rn.Start()
		defer rn.Stop()
	}

	time.Sleep(500 * time.Millisecond)

	getLeader := func() *raft.RaftNode {
		for _, rn := range nodes {
			role, _, _, _ := rn.GetState()
			if role == raft.Leader {
				return rn
			}
		}
		return nil
	}

	leader := getLeader()
	if leader == nil {
		panic("no leader elected for latency benchmark")
	}

	iterations := 1000
	latencies := make([]time.Duration, 0, iterations)
	payload := []byte("standard-replicated-commit-log-payload-128b-benchmark-data")

	fmt.Printf("\nExecuting %d synchronous round-trip write-to-quorum proposals...\n", iterations)
	startTotal := time.Now()
	for i := 0; i < iterations; i++ {
		currentLeader := getLeader()
		if currentLeader == nil {
			time.Sleep(50 * time.Millisecond)
			currentLeader = getLeader()
		}

		t0 := time.Now()
		_, err := currentLeader.Propose(payload)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		latencies = append(latencies, time.Since(t0))
	}
	totalElapsed := time.Since(startTotal)

	if len(latencies) == 0 {
		panic("no successful proposals recorded")
	}

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	n := len(latencies)
	p50 := latencies[int(float64(n)*0.50)]
	p95 := latencies[int(float64(n)*0.95)]
	p99 := latencies[int(float64(n)*0.99)]
	max := latencies[n-1]

	fmt.Println("\n[Replication Latency Percentiles (p50, p95, p99)]")
	fmt.Printf("  • Total Ops:  %d writes committed\n", n)
	fmt.Printf("  • Total Time: %.2f ms\n", float64(totalElapsed.Microseconds())/1000.0)
	fmt.Printf("  • p50 (Median):   %.3f ms\n", float64(p50.Microseconds())/1000.0)
	fmt.Printf("  • p95:            %.3f ms\n", float64(p95.Microseconds())/1000.0)
	fmt.Printf("  • p99:            %.3f ms\n", float64(p99.Microseconds())/1000.0)
	fmt.Printf("  • Max Latency:    %.3f ms\n", float64(max.Microseconds())/1000.0)
	fmt.Println("\n================================================================")
}
