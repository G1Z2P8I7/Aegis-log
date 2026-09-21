package network

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// TLSBundle holds the generated certificates and keys for mTLS communication.
type TLSBundle struct {
	CACertPEM   []byte
	CAKeyPEM    []byte
	NodeCertPEM map[string][]byte
	NodeKeyPEM  map[string][]byte
	NodeTLS     map[string]tls.Certificate
}

// GenerateClusterCerts creates a root CA and individual node certificates for all specified node IDs.
func GenerateClusterCerts(nodeIDs []string) (*TLSBundle, error) {
	caCert, caKey, caCertPEM, caKeyPEM, err := GenerateCA("Aegis Cluster Root CA")
	if err != nil {
		return nil, fmt.Errorf("failed to generate root CA: %w", err)
	}

	bundle := &TLSBundle{
		CACertPEM:   caCertPEM,
		CAKeyPEM:    caKeyPEM,
		NodeCertPEM: make(map[string][]byte),
		NodeKeyPEM:  make(map[string][]byte),
		NodeTLS:     make(map[string]tls.Certificate),
	}

	defaultHosts := []string{"127.0.0.1", "localhost", "::1"}

	for _, nodeID := range nodeIDs {
		hosts := append(defaultHosts, fmt.Sprintf("node-%s", nodeID), nodeID)
		cert, certPEM, keyPEM, err := GenerateNodeCert(caCert, caKey, fmt.Sprintf("aegis-node-%s", nodeID), hosts)
		if err != nil {
			return nil, fmt.Errorf("failed to generate node cert for %s: %w", nodeID, err)
		}
		bundle.NodeCertPEM[nodeID] = certPEM
		bundle.NodeKeyPEM[nodeID] = keyPEM
		bundle.NodeTLS[nodeID] = cert
	}

	return bundle, nil
}

// GenerateCA generates a self-signed root X.509 Certificate Authority.
func GenerateCA(commonName string) (*x509.Certificate, *rsa.PrivateKey, []byte, []byte, error) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:  []string{"Aegis Distributed Systems"},
			CommonName:    commonName,
			Country:       []string{"US"},
			Province:      []string{"CA"},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"Consensus Way"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes := x509.MarshalPKCS1PrivateKey(privKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})

	return cert, privKey, certPEM, keyPEM, nil
}

// GenerateNodeCert creates an end-entity certificate signed by the CA for mutual TLS.
func GenerateNodeCert(caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, hosts []string) (tls.Certificate, []byte, []byte, error) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Aegis Distributed Systems"},
			CommonName:   commonName,
		},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, caCert, &privKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes := x509.MarshalPKCS1PrivateKey(privKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	return tlsCert, certPEM, keyPEM, nil
}

// CreateServerTLSConfig builds an mTLS server configuration requiring trusted client certificates.
func CreateServerTLSConfig(cert tls.Certificate, caCertPEM []byte) (*tls.Config, error) {
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate into pool")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// CreateClientTLSConfig builds an mTLS client configuration presenting identity to servers.
func CreateClientTLSConfig(cert tls.Certificate, caCertPEM []byte, serverName string) (*tls.Config, error) {
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate into pool")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}

	if serverName != "" {
		if net.ParseIP(serverName) == nil {
			cfg.ServerName = serverName
		}
	}

	return cfg, nil
}
