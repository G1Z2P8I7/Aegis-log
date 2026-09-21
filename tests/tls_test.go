package tests

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"testing"
	"time"

	"KhorosLog/network"
	"KhorosLog/raft"
)

func TestTLS_MutualAuthentication(t *testing.T) {
	// 1. Generate Root CA
	caCert, caKey, caPEM, _, err := network.GenerateCA("Aegis Cluster Test CA")
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	// 2. Generate Server Cert
	serverTLSCert, _, _, err := network.GenerateNodeCert(caCert, caKey, "node-server", []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("failed to generate server cert: %v", err)
	}

	// 3. Generate Valid Client Cert
	validClientTLSCert, _, _, err := network.GenerateNodeCert(caCert, caKey, "node-client-valid", []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("failed to generate valid client cert: %v", err)
	}

	// 4. Generate Untrusted Rogue CA and Rogue Client Cert
	rogueCACert, rogueCAKey, _, _, err := network.GenerateCA("Rogue Root CA")
	if err != nil {
		t.Fatalf("failed to generate rogue CA: %v", err)
	}
	rogueClientTLSCert, _, _, err := network.GenerateNodeCert(rogueCACert, rogueCAKey, "node-client-rogue", []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("failed to generate rogue client cert: %v", err)
	}

	serverTLS, err := network.CreateServerTLSConfig(serverTLSCert, caPEM)
	if err != nil {
		t.Fatalf("failed to create server TLS config: %v", err)
	}

	// Bind a TLS listener on an ephemeral port
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("failed to start TLS listener: %v", err)
	}
	defer ln.Close()

	serverAddr := ln.Addr().String()

	// Server goroutine: accepts connections and echoes
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tlsConn, ok := c.(*tls.Conn)
				if ok {
					if err := tlsConn.Handshake(); err != nil {
						return
					}
					// Verify client provided certificate
					state := tlsConn.ConnectionState()
					if len(state.PeerCertificates) == 0 {
						return
					}
				}
				buf := make([]byte, 10)
				n, _ := c.Read(buf)
				if n > 0 {
					_, _ = c.Write([]byte("PONG:" + string(buf[:n])))
				}
			}(conn)
		}
	}()

	// CASE 1: Valid client with signed cert connects successfully over mTLS
	t.Run("ValidClientAuthorized", func(t *testing.T) {
		clientTLS, err := network.CreateClientTLSConfig(validClientTLSCert, caPEM, "localhost")
		if err != nil {
			t.Fatalf("failed to create client TLS config: %v", err)
		}

		conn, err := tls.Dial("tcp", serverAddr, clientTLS)
		if err != nil {
			t.Fatalf("valid client failed to connect: %v", err)
		}
		defer conn.Close()

		// Verify TLS 1.3 negotiated
		if conn.ConnectionState().Version != tls.VersionTLS13 {
			t.Fatalf("expected TLS 1.3, got version 0x%04x", conn.ConnectionState().Version)
		}

		_, err = conn.Write([]byte("PING"))
		if err != nil {
			t.Fatalf("failed to write data: %v", err)
		}

		buf := make([]byte, 20)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("failed to read response: %v", err)
		}
		if string(buf[:n]) != "PONG:PING" {
			t.Fatalf("unexpected response: %s", string(buf[:n]))
		}
	})

	// CASE 2: Plaintext client (no TLS) must be rejected
	t.Run("PlaintextClientRejected", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", serverAddr, 200*time.Millisecond)
		if err != nil {
			return // Connection refused is acceptable
		}
		defer conn.Close()

		_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
		_, _ = conn.Write([]byte("HELLO RAW"))
		buf := make([]byte, 20)
		_, readErr := conn.Read(buf)
		if readErr == nil {
			t.Fatalf("expected plaintext connection to fail or be closed by server")
		}
	})

	// CASE 3: Rogue client with cert signed by untrusted CA must be rejected
	t.Run("RogueClientRejected", func(t *testing.T) {
		clientTLS, err := network.CreateClientTLSConfig(rogueClientTLSCert, caPEM, "localhost")
		if err != nil {
			t.Fatalf("failed to create rogue client TLS config: %v", err)
		}

		conn, err := tls.Dial("tcp", serverAddr, clientTLS)
		if err == nil {
			// Handshake must fail on read/write
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, err = conn.Write([]byte("PING"))
			if err == nil {
				buf := make([]byte, 20)
				_, err = conn.Read(buf)
			}
			conn.Close()
			if err == nil {
				t.Fatalf("expected rogue client handshake/read to fail due to untrusted client cert")
			}
		}
	})

	// CASE 4: Client without certificate must be rejected (Server requires client cert)
	t.Run("MissingClientCertRejected", func(t *testing.T) {
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caPEM)

		noCertTLS := &tls.Config{
			RootCAs:    caPool,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS13,
		}

		conn, err := tls.Dial("tcp", serverAddr, noCertTLS)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, err = conn.Write([]byte("PING"))
			if err == nil {
				buf := make([]byte, 20)
				_, err = conn.Read(buf)
			}
			conn.Close()
			if err == nil {
				t.Fatalf("expected client with missing cert to fail server RequireAndVerifyClientCert")
			}
		}
	})
}

func TestTLS_RaftClusterOverMTLS(t *testing.T) {
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	bundle, err := network.GenerateClusterCerts(nodeIDs)
	if err != nil {
		t.Fatalf("failed to generate cluster certs: %v", err)
	}

	// Allocate 3 free ports for TCP transports
	ports := make([]int, 3)
	listeners := make([]net.Listener, 3)
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to reserve port: %v", err)
		}
		ports[i] = l.Addr().(*net.TCPAddr).Port
		listeners[i] = l
	}
	for _, l := range listeners {
		l.Close()
	}

	nodeAddrs := make(map[string]string)
	for i, id := range nodeIDs {
		nodeAddrs[id] = fmt.Sprintf("127.0.0.1:%d", ports[i])
	}

	transports := make(map[string]*raft.TCPTransport)
	nodes := make(map[string]*raft.RaftNode)

	for _, id := range nodeIDs {
		var peerAddrs []string
		for _, peerID := range nodeIDs {
			if peerID != id {
				peerAddrs = append(peerAddrs, nodeAddrs[peerID])
			}
		}

		sTLS, err := network.CreateServerTLSConfig(bundle.NodeTLS[id], bundle.CACertPEM)
		if err != nil {
			t.Fatalf("failed server tls for %s: %v", id, err)
		}
		cTLS, err := network.CreateClientTLSConfig(bundle.NodeTLS[id], bundle.CACertPEM, "127.0.0.1")
		if err != nil {
			t.Fatalf("failed client tls for %s: %v", id, err)
		}

		tt, err := raft.NewTCPTransportWithTLS(nodeAddrs[id], sTLS, cTLS)
		if err != nil {
			t.Fatalf("failed to create TLS transport for %s: %v", id, err)
		}
		transports[id] = tt

		cfg := raft.Config{
			ID:                id,
			Peers:             peerAddrs,
			ElectionMinMs:     120,
			ElectionMaxMs:     240,
			HeartbeatInterval: 30 * time.Millisecond,
			Transport:         tt,
		}
		rn := raft.NewRaftNode(cfg)
		tt.RegisterNode(rn)
		nodes[id] = rn
	}

	for _, rn := range nodes {
		rn.Start()
		defer rn.Stop()
	}
	for _, tt := range transports {
		defer tt.Close()
	}

	// Wait for leader election over mTLS
	time.Sleep(500 * time.Millisecond)

	var leader *raft.RaftNode
	for _, rn := range nodes {
		role, _, _, _ := rn.GetState()
		if role == raft.Leader {
			leader = rn
			break
		}
	}

	if leader == nil {
		t.Fatalf("failed to elect a leader over mutual TLS")
	}

	// Propose entries across mTLS channels
	for i := 1; i <= 3; i++ {
		idx, err := leader.Propose([]byte(fmt.Sprintf("mtls-secured-log-%d", i)))
		if err != nil {
			t.Fatalf("failed to replicate entry %d over mTLS: %v", i, err)
		}
		if idx != uint64(i) {
			t.Fatalf("expected commit index %d, got %d", i, idx)
		}
	}

	t.Logf("Raft cluster successfully elected leader and replicated logs over TLS 1.3 mutual auth!")
}
