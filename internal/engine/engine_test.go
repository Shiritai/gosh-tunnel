package engine_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"gosh-tunnel/internal/engine"
)

// generateTestKey creates a temporary RSA private key file.
func generateTestKey(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_rsa")
	file, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("Failed to create key file: %v", err)
	}
	defer file.Close()
	if err := pem.Encode(file, &privBlock); err != nil {
		t.Fatalf("Failed to write key to file: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	return keyPath, signer
}

// startMockSSHServer starts a basic SSH server that accepts connections.
func startMockSSHServer(t *testing.T, signer ssh.Signer) (string, net.Listener) {
	t.Helper()

	config := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen for SSH server: %v", err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // Closed
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					newChannel.Reject(ssh.Prohibited, "no channels allowed in engine test")
				}
			}()
		}
	}()

	return listener.Addr().String(), listener
}

func TestEngineConnectAndReconnect(t *testing.T) {
	keyPath, signer := generateTestKey(t)
	addr, listener := startMockSSHServer(t, signer)
	
	host, port, _ := net.SplitHostPort(addr)

	eng := engine.New(host, port, "testuser", keyPath)

	// Test initial connection
	client, err := eng.GetClient()
	if err != nil {
		t.Fatalf("GetClient failed on initial connect: %v", err)
	}
	if client == nil {
		t.Fatalf("GetClient returned nil client")
	}

	// Calling GetClient again should return the same client
	client2, err := eng.GetClient()
	if err != nil {
		t.Fatalf("GetClient failed on second call: %v", err)
	}
	if client != client2 {
		t.Fatalf("Expected same client instance returned")
	}

	// Simulate disconnect by closing the client connection directly
	client.Close()

	// GetClient should reconnect and return a new client
	client3, err := eng.GetClient()
	if err != nil {
		t.Fatalf("GetClient failed on reconnect: %v", err)
	}
	if client3 == client {
		t.Fatalf("Expected a new client instance after disconnect, got the same one")
	}

	// Close listener to simulate server down
	listener.Close()
	client3.Close()

	// Connect should fail now
	_, err = eng.GetClient()
	if err == nil {
		t.Fatalf("GetClient should have failed when server is down")
	}
}
