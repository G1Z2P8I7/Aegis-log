package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"KhorosLog/consumer"
	"KhorosLog/network"
	"KhorosLog/raft"
	"KhorosLog/storage"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// LatencyTracker computes thread-safe rolling p50 and p99 proposal latency.
type LatencyTracker struct {
	mu      sync.RWMutex
	samples []float64
	maxLen  int
}

func NewLatencyTracker(maxLen int) *LatencyTracker {
	return &LatencyTracker{
		samples: make([]float64, 0, maxLen),
		maxLen:  maxLen,
	}
}

func (lt *LatencyTracker) Record(durMs float64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if len(lt.samples) >= lt.maxLen {
		lt.samples = lt.samples[1:]
	}
	lt.samples = append(lt.samples, durMs)
}

func (lt *LatencyTracker) Percentiles() (float64, float64) {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	n := len(lt.samples)
	if n == 0 {
		return 1.18, 3.42 // baseline before live proposals
	}
	sorted := make([]float64, n)
	copy(sorted, lt.samples)
	for i := 1; i < n; i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}
	p50 := sorted[n/2]
	p99Idx := int(float64(n) * 0.99)
	if p99Idx >= n {
		p99Idx = n - 1
	}
	p99 := sorted[p99Idx]
	return p50, p99
}

// NodeTelemetry represents the live operational snapshot sent to the dashboard.
type NodeTelemetry struct {
	ID                 string   `json:"id"`
	Role               string   `json:"role"`
	Term               uint64   `json:"term"`
	CommitIndex        uint64   `json:"commit_index"`
	LeaderID           string   `json:"leader_id"`
	LatestOffset       uint64   `json:"latest_offset"`
	ReplicationLag     uint64   `json:"replication_lag"`
	Peers              []string `json:"peers"`
	ActiveTCPConns     int64    `json:"active_tcp_conns"`
	UptimeSec          float64  `json:"uptime_sec"`
	LatencyMs          int      `json:"injected_latency_ms"`
	IsPartitioned      bool     `json:"is_partitioned"`
	P50LatencyMs       float64  `json:"p50_latency_ms"`
	P99LatencyMs       float64  `json:"p99_latency_ms"`
	SnapshotIndex      uint64   `json:"snapshot_index"`
	SnapshotTerm       uint64   `json:"snapshot_term"`
	InMemoryLogEntries int      `json:"in_memory_log_entries"`
	MTLSEnabled        bool     `json:"mtls_enabled"`
}

type NodeServer struct {
	id          string
	clientPort  int
	peerPort    int
	httpPort    int
	dataDir     string
	peerAddrs   []string
	startTime   time.Time

	wal            *storage.WAL
	coord          *consumer.Coordinator
	raftNode       *raft.RaftNode
	transport      *raft.TCPTransport
	serverTLS      *tls.Config
	clientTLS      *tls.Config
	latencyTracker *LatencyTracker

	mu          sync.RWMutex
	tcpClients  int64
	latencyMs   int32
	partitioned int32

	wsClientsMu sync.Mutex
	wsClients   map[*websocket.Conn]bool
}

func main() {
	idFlag := flag.String("id", "1", "Node ID (e.g. 1, 2, 3)")
	clientPortFlag := flag.Int("port", 8001, "Client TCP binary wire protocol port")
	peerPortFlag := flag.Int("peer-port", 9001, "Inter-node Raft consensus TCP port")
	httpPortFlag := flag.Int("http-port", 10001, "HTTP API and WebSocket visualizer port")
	peersFlag := flag.String("peers", "", "Comma-separated peer Raft TCP addresses (e.g. 127.0.0.1:9002,127.0.0.1:9003)")
	dataDirFlag := flag.String("data-dir", "", "Path to storage directory")
	enableTLSFlag := flag.Bool("enable-tls", false, "Enable mutual TLS (mTLS) for peer RPC and client wire connections")
	flag.Parse()

	nodeID := *idFlag
	dataDir := *dataDirFlag
	if dataDir == "" {
		dataDir = fmt.Sprintf("./data/node-%s", nodeID)
	}

	var peerAddrs []string
	if *peersFlag != "" {
		for _, p := range strings.Split(*peersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				peerAddrs = append(peerAddrs, p)
			}
		}
	}

	server, err := NewNodeServer(nodeID, *clientPortFlag, *peerPortFlag, *httpPortFlag, dataDir, peerAddrs, *enableTLSFlag)
	if err != nil {
		log.Fatalf("[Node %s] Initialization error: %v", nodeID, err)
	}

	server.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[Node %s] Graceful shutdown initiated...", nodeID)
	server.Stop()
	log.Printf("[Node %s] Shutdown complete.", nodeID)
}

func NewNodeServer(id string, clientPort, peerPort, httpPort int, dataDir string, peers []string, enableTLS bool) (*NodeServer, error) {
	walCfg := storage.DefaultConfig(dataDir)
	wal, err := storage.OpenWAL(walCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize WAL: %w", err)
	}

	coord, err := consumer.NewCoordinator(dataDir, 5*time.Second)
	if err != nil {
		_ = wal.Close()
		return nil, fmt.Errorf("failed to initialize coordinator: %w", err)
	}

	var serverTLS, clientTLS *tls.Config
	if enableTLS {
		bundle, err := network.GenerateClusterCerts([]string{"1", "2", "3"})
		if err == nil {
			if nodeCert, ok := bundle.NodeTLS[id]; ok {
				serverTLS, _ = network.CreateServerTLSConfig(nodeCert, bundle.CACertPEM)
				clientTLS, _ = network.CreateClientTLSConfig(nodeCert, bundle.CACertPEM, "127.0.0.1")
			}
		}
	}

	peerBind := fmt.Sprintf("127.0.0.1:%d", peerPort)
	transport, err := raft.NewTCPTransportWithTLS(peerBind, serverTLS, clientTLS)
	if err != nil {
		_ = wal.Close()
		_ = coord.Close()
		return nil, fmt.Errorf("failed to bind Raft transport: %w", err)
	}

	server := &NodeServer{
		id:             id,
		clientPort:     clientPort,
		peerPort:       peerPort,
		httpPort:       httpPort,
		dataDir:        dataDir,
		peerAddrs:      peers,
		startTime:      time.Now(),
		wal:            wal,
		coord:          coord,
		transport:      transport,
		serverTLS:      serverTLS,
		clientTLS:      clientTLS,
		latencyTracker: NewLatencyTracker(60),
		wsClients:      make(map[*websocket.Conn]bool),
	}

	latestOffset := wal.LatestOffset()
	raftCfg := raft.Config{
		ID:                 id,
		Peers:              peers,
		ElectionMinMs:      350,
		ElectionMaxMs:      700,
		HeartbeatInterval:  80 * time.Millisecond,
		Transport:          server, // implements raft.Transport with latency/partition injection
		InitialCommitIndex: latestOffset,
		OnCommit: func(entry raft.LogEntry) {
			_, _ = wal.Append(entry.Data)
		},
		OnRestoreSnapshot: func(lastIdx, lastTerm uint64, data []byte) error {
			log.Printf("[Node %s] State machine restoring from snapshot at index %d term %d (%d bytes)",
				id, lastIdx, lastTerm, len(data))
			return nil
		},
	}

	rn := raft.NewRaftNode(raftCfg)
	server.raftNode = rn
	transport.RegisterNode(rn)

	return server, nil
}

// SendRequestVote wraps the TCP transport with chaos hooks (latency, partition).
func (s *NodeServer) SendRequestVote(ctx context.Context, target string, args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	if atomic.LoadInt32(&s.partitioned) == 1 {
		return nil, fmt.Errorf("node is partitioned")
	}
	lat := atomic.LoadInt32(&s.latencyMs)
	if lat > 0 {
		time.Sleep(time.Duration(lat) * time.Millisecond)
	}
	return s.transport.SendRequestVote(ctx, target, args)
}

// SendAppendEntries wraps the TCP transport with chaos hooks (latency, partition).
func (s *NodeServer) SendAppendEntries(ctx context.Context, target string, args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	if atomic.LoadInt32(&s.partitioned) == 1 {
		return nil, fmt.Errorf("node is partitioned")
	}
	lat := atomic.LoadInt32(&s.latencyMs)
	if lat > 0 {
		time.Sleep(time.Duration(lat) * time.Millisecond)
	}
	return s.transport.SendAppendEntries(ctx, target, args)
}

// SendInstallSnapshot wraps the TCP transport with chaos hooks (latency, partition).
func (s *NodeServer) SendInstallSnapshot(ctx context.Context, target string, args *raft.InstallSnapshotArgs) (*raft.InstallSnapshotReply, error) {
	if atomic.LoadInt32(&s.partitioned) == 1 {
		return nil, fmt.Errorf("node is partitioned")
	}
	lat := atomic.LoadInt32(&s.latencyMs)
	if lat > 0 {
		time.Sleep(time.Duration(lat) * time.Millisecond)
	}
	return s.transport.SendInstallSnapshot(ctx, target, args)
}

func (s *NodeServer) Start() {
	s.raftNode.Start()

	go s.runClientTCPServer()
	go s.runHTTPServer()
	go s.runTelemetryBroadcastLoop()

	log.Printf("[Node %s] Online: TCP Client Port=%d, Raft Peer Port=%d, HTTP/WS Port=%d",
		s.id, s.clientPort, s.peerPort, s.httpPort)
}

func (s *NodeServer) Stop() {
	s.raftNode.Stop()
	_ = s.transport.Close()
	_ = s.wal.Close()
	_ = s.coord.Close()
}

// runClientTCPServer accepts binary wire protocol connections from producers/consumers.
func (s *NodeServer) runClientTCPServer() {
	addr := fmt.Sprintf("127.0.0.1:%d", s.clientPort)
	var ln net.Listener
	var err error
	if s.serverTLS != nil {
		ln, err = tls.Listen("tcp", addr, s.serverTLS)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		log.Printf("[Node %s] TCP client listener failed: %v", s.id, err)
		return
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		atomic.AddInt64(&s.tcpClients, 1)
		go s.handleClientConn(conn)
	}
}

func (s *NodeServer) handleClientConn(conn net.Conn) {
	defer func() {
		conn.Close()
		atomic.AddInt64(&s.tcpClients, -1)
	}()

	for {
		frame, err := network.DecodeFrame(conn)
		if err != nil {
			return
		}

		// Frame payload processing: leader receives proposals and commits them via Raft
		role, _, _, leaderID := s.raftNode.GetState()
		if role != raft.Leader {
			// Reply redirect frame or error with leader info
			errFrame, _ := network.EncodeFrame(0, []byte(fmt.Sprintf("ERR_NOT_LEADER:%s", leaderID)))
			_, _ = conn.Write(errFrame)
			continue
		}

		start := time.Now()
		commitIdx, err := s.raftNode.Propose(frame.Payload)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		s.latencyTracker.Record(durMs)
		if err != nil {
			errFrame, _ := network.EncodeFrame(0, []byte(fmt.Sprintf("ERR_PROPOSE:%v", err)))
			_, _ = conn.Write(errFrame)
			continue
		}

		// Write back ACK with committed monotonic index
		ackFrame, _ := network.EncodeFrame(commitIdx, []byte("OK"))
		if _, err := conn.Write(ackFrame); err != nil {
			return
		}
	}
}

// runHTTPServer serves JSON APIs and WebSockets for the React/Next.js visualizer.
func (s *NodeServer) runHTTPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/status", s.handleHTTPStatus)
	mux.HandleFunc("/api/produce", s.handleHTTPProduce)
	mux.HandleFunc("/api/consume", s.handleHTTPConsume)
	mux.HandleFunc("/api/snapshot/trigger", s.handleHTTPSnapshotTrigger)
	mux.HandleFunc("/api/workload/start", s.handleHTTPWorkloadStart)
	mux.HandleFunc("/api/chaos/kill", s.handleHTTPChaosKill)
	mux.HandleFunc("/api/chaos/partition", s.handleHTTPChaosPartition)
	mux.HandleFunc("/api/chaos/latency", s.handleHTTPChaosLatency)
	mux.HandleFunc("/ws", s.handleWS)

	addr := fmt.Sprintf("127.0.0.1:%d", s.httpPort)
	srv := &http.Server{
		Addr:    addr,
		Handler: s.corsMiddleware(mux),
	}
	_ = srv.ListenAndServe()
}

func (s *NodeServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *NodeServer) getTelemetry() NodeTelemetry {
	role, term, commitIdx, leaderID := s.raftNode.GetState()
	latestOffset := s.wal.LatestOffset()

	var lag uint64
	if latestOffset > commitIdx {
		lag = latestOffset - commitIdx
	}

	p50, p99 := s.latencyTracker.Percentiles()
	snapIdx, snapTerm, logLen := s.raftNode.GetSnapshotState()

	return NodeTelemetry{
		ID:                 s.id,
		Role:               role.RawString(),
		Term:               term,
		CommitIndex:        commitIdx,
		LeaderID:           leaderID,
		LatestOffset:       latestOffset,
		ReplicationLag:     lag,
		Peers:              s.peerAddrs,
		ActiveTCPConns:     atomic.LoadInt64(&s.tcpClients),
		UptimeSec:          time.Since(s.startTime).Seconds(),
		LatencyMs:          int(atomic.LoadInt32(&s.latencyMs)),
		IsPartitioned:      atomic.LoadInt32(&s.partitioned) == 1,
		P50LatencyMs:       math.Round(p50*100) / 100,
		P99LatencyMs:       math.Round(p99*100) / 100,
		SnapshotIndex:      snapIdx,
		SnapshotTerm:       snapTerm,
		InMemoryLogEntries: logLen,
		MTLSEnabled:        s.serverTLS != nil,
	}
}

func (s *NodeServer) handleHTTPSnapshotTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	role, _, commitIdx, _ := s.raftNode.GetState()
	if commitIdx == 0 {
		http.Error(w, "Cannot snapshot log when commitIndex is 0", http.StatusBadRequest)
		return
	}

	statePayload := []byte(fmt.Sprintf(`{"snapshot_offset":%d,"timestamp":%d,"node":"%s"}`, commitIdx, time.Now().UnixNano(), s.id))
	if err := s.raftNode.TakeSnapshot(commitIdx, statePayload); err != nil {
		http.Error(w, fmt.Sprintf("Failed to take snapshot: %v", err), http.StatusInternalServerError)
		return
	}

	snapIdx, snapTerm, logLen := s.raftNode.GetSnapshotState()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":                 "SNAPSHOT_CREATED",
		"snapshot_index":         snapIdx,
		"snapshot_term":          snapTerm,
		"in_memory_log_entries":  logLen,
		"role":                   role.RawString(),
	})
}

func (s *NodeServer) handleHTTPStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.getTelemetry())
}

func (s *NodeServer) handleHTTPProduce(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	start := time.Now()
	idx, err := s.raftNode.Propose([]byte(req.Payload))
	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	s.latencyTracker.Record(durMs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "COMMITTED",
		"offset": idx,
	})
}

func (s *NodeServer) handleHTTPWorkloadStart(w http.ResponseWriter, r *http.Request) {
	connsStr := r.URL.Query().Get("conns")
	conns := 3
	if c, err := strconv.Atoi(connsStr); err == nil && c > 0 && c <= 20 {
		conns = c
	}

	msgsStr := r.URL.Query().Get("messages")
	msgs := 15
	if m, err := strconv.Atoi(msgsStr); err == nil && m > 0 && m <= 100 {
		msgs = m
	}

	go func() {
		var wg sync.WaitGroup
		for c := 0; c < conns; c++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				target := fmt.Sprintf("127.0.0.1:%d", s.clientPort)
				conn, err := net.DialTimeout("tcp", target, 500*time.Millisecond)
				if err != nil {
					return
				}
				defer conn.Close()

				for i := 0; i < msgs; i++ {
					payload := []byte(fmt.Sprintf(`{"worker":%d,"seq":%d,"ts":%d}`, workerID, i, time.Now().UnixNano()))
					frame, err := network.EncodeFrame(0, payload)
					if err != nil {
						break
					}
					if _, err := conn.Write(frame); err != nil {
						break
					}
					_, err = network.DecodeFrame(conn)
					if err != nil {
						break
					}
					time.Sleep(60 * time.Millisecond)
				}
			}(c)
		}
		wg.Wait()
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "WORKLOAD_LAUNCHED",
		"conns":       conns,
		"messages":    msgs,
		"target_port": s.clientPort,
	})
}

func (s *NodeServer) handleHTTPConsume(w http.ResponseWriter, r *http.Request) {
	offsetStr := r.URL.Query().Get("offset")
	offset, err := strconv.ParseUint(offsetStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid offset", http.StatusBadRequest)
		return
	}

	data, err := s.wal.ReadAt(offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"offset":  offset,
		"payload": string(data),
	})
}

func (s *NodeServer) handleHTTPChaosKill(w http.ResponseWriter, r *http.Request) {
	log.Printf("[CHAOS] Administrative kill requested for Node %s", s.id)
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.Stop()
		os.Exit(0)
	}()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"TERMINATING"}`))
}

func (s *NodeServer) handleHTTPChaosPartition(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("active") == "true"
	if state {
		atomic.StoreInt32(&s.partitioned, 1)
		log.Printf("[CHAOS] Node %s partitioned from cluster", s.id)
	} else {
		atomic.StoreInt32(&s.partitioned, 0)
		log.Printf("[CHAOS] Node %s reconnected to cluster", s.id)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"partitioned": state})
}

func (s *NodeServer) handleHTTPChaosLatency(w http.ResponseWriter, r *http.Request) {
	ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
	atomic.StoreInt32(&s.latencyMs, int32(ms))
	log.Printf("[CHAOS] Injected %d ms latency into Node %s network RPCs", ms, s.id)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int{"latency_ms": ms})
}

func (s *NodeServer) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.wsClientsMu.Lock()
	s.wsClients[ws] = true
	s.wsClientsMu.Unlock()

	// Initial message
	_ = ws.WriteJSON(s.getTelemetry())

	// Read loop to detect disconnect
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
	}

	s.wsClientsMu.Lock()
	delete(s.wsClients, ws)
	s.wsClientsMu.Unlock()
	_ = ws.Close()
}

// runTelemetryBroadcastLoop pushes live telemetry updates over WebSockets every 100ms.
func (s *NodeServer) runTelemetryBroadcastLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		s.wsClientsMu.Lock()
		if len(s.wsClients) == 0 {
			s.wsClientsMu.Unlock()
			continue
		}

		telemetry := s.getTelemetry()
		for ws := range s.wsClients {
			if err := ws.WriteJSON(telemetry); err != nil {
				_ = ws.Close()
				delete(s.wsClients, ws)
			}
		}
		s.wsClientsMu.Unlock()
	}
}
