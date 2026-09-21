package raft

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// RPCType indicates the type of internal Raft peer message.
type RPCType byte

const (
	RPCRequestVote      RPCType = 0x10
	RPCAppendEntries    RPCType = 0x20
	RPCInstallSnapshot  RPCType = 0x30
)

// NetworkRPCMessage is the wire envelope for inter-node consensus RPCs.
type NetworkRPCMessage struct {
	Type RPCType         `json:"type"`
	Body json.RawMessage `json:"body"`
}

// TCPTransport implements peer-to-peer TCP communication for Raft cluster nodes with optional mTLS.
type TCPTransport struct {
	mu          sync.RWMutex
	bindAddr    string
	listener    net.Listener
	handlerNode *RaftNode
	stopCh      chan struct{}
	clientPool  map[string]net.Conn
	serverTLS   *tls.Config
	clientTLS   *tls.Config
}

// NewTCPTransport initializes a TCP peer listener on bindAddr without TLS.
func NewTCPTransport(bindAddr string) (*TCPTransport, error) {
	return NewTCPTransportWithTLS(bindAddr, nil, nil)
}

// NewTCPTransportWithTLS initializes a TCP peer listener on bindAddr with optional mTLS configurations.
func NewTCPTransportWithTLS(bindAddr string, serverTLS, clientTLS *tls.Config) (*TCPTransport, error) {
	var ln net.Listener
	var err error

	if serverTLS != nil {
		ln, err = tls.Listen("tcp", bindAddr, serverTLS)
	} else {
		ln, err = net.Listen("tcp", bindAddr)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to bind Raft TCP listener on %s: %w", bindAddr, err)
	}

	tt := &TCPTransport{
		bindAddr:   bindAddr,
		listener:   ln,
		stopCh:     make(chan struct{}),
		clientPool: make(map[string]net.Conn),
		serverTLS:  serverTLS,
		clientTLS:  clientTLS,
	}

	go tt.acceptLoop()
	return tt, nil
}

// RegisterNode binds the local RaftNode to handle incoming RPCs.
func (tt *TCPTransport) RegisterNode(rn *RaftNode) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.handlerNode = rn
}

func (tt *TCPTransport) acceptLoop() {
	for {
		conn, err := tt.listener.Accept()
		if err != nil {
			select {
			case <-tt.stopCh:
				return
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		go tt.handleConnection(conn)
	}
}

func (tt *TCPTransport) handleConnection(conn net.Conn) {
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		var env NetworkRPCMessage
		if err := decoder.Decode(&env); err != nil {
			return
		}

		tt.mu.RLock()
		node := tt.handlerNode
		tt.mu.RUnlock()

		if node == nil {
			return
		}

		switch env.Type {
		case RPCRequestVote:
			var args RequestVoteArgs
			if err := json.Unmarshal(env.Body, &args); err != nil {
				return
			}
			reply := node.HandleRequestVote(&args)
			_ = encoder.Encode(reply)

		case RPCAppendEntries:
			var args AppendEntriesArgs
			if err := json.Unmarshal(env.Body, &args); err != nil {
				return
			}
			reply := node.HandleAppendEntries(&args)
			_ = encoder.Encode(reply)

		case RPCInstallSnapshot:
			var args InstallSnapshotArgs
			if err := json.Unmarshal(env.Body, &args); err != nil {
				return
			}
			reply := node.HandleInstallSnapshot(&args)
			_ = encoder.Encode(reply)
		}
	}
}

func (tt *TCPTransport) dial(ctx context.Context, target string) (net.Conn, error) {
	d := net.Dialer{Timeout: 250 * time.Millisecond}
	if tt.clientTLS != nil {
		tlsDialer := tls.Dialer{
			NetDialer: &d,
			Config:    tt.clientTLS,
		}
		return tlsDialer.DialContext(ctx, "tcp", target)
	}
	return d.DialContext(ctx, "tcp", target)
}

// SendRequestVote transmits a vote request to target peer over TCP.
func (tt *TCPTransport) SendRequestVote(ctx context.Context, target string, args *RequestVoteArgs) (*RequestVoteReply, error) {
	conn, err := tt.dial(ctx, target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	body, _ := json.Marshal(args)
	env := NetworkRPCMessage{
		Type: RPCRequestVote,
		Body: body,
	}

	if err := json.NewEncoder(conn).Encode(&env); err != nil {
		return nil, err
	}

	var reply RequestVoteReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return nil, err
	}

	return &reply, nil
}

// SendAppendEntries transmits an AppendEntries / heartbeat to target peer over TCP.
func (tt *TCPTransport) SendAppendEntries(ctx context.Context, target string, args *AppendEntriesArgs) (*AppendEntriesReply, error) {
	conn, err := tt.dial(ctx, target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	body, _ := json.Marshal(args)
	env := NetworkRPCMessage{
		Type: RPCAppendEntries,
		Body: body,
	}

	if err := json.NewEncoder(conn).Encode(&env); err != nil {
		return nil, err
	}

	var reply AppendEntriesReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("connection closed by peer")
		}
		return nil, err
	}

	return &reply, nil
}

// SendInstallSnapshot transmits a snapshot installation to target peer over TCP (Raft §7).
func (tt *TCPTransport) SendInstallSnapshot(ctx context.Context, target string, args *InstallSnapshotArgs) (*InstallSnapshotReply, error) {
	conn, err := tt.dial(ctx, target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	body, _ := json.Marshal(args)
	env := NetworkRPCMessage{
		Type: RPCInstallSnapshot,
		Body: body,
	}

	if err := json.NewEncoder(conn).Encode(&env); err != nil {
		return nil, err
	}

	var reply InstallSnapshotReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("connection closed by peer")
		}
		return nil, err
	}

	return &reply, nil
}

// Close terminates the transport listener.
func (tt *TCPTransport) Close() error {
	close(tt.stopCh)
	return tt.listener.Close()
}
