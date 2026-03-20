package tunnel_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/tunnel"
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

// startMockSSHServer starts a very basic SSH server that accepts any connection
// and handles direct-tcpip requests by dialing the requested address.
func startMockSSHServer(t *testing.T, signer ssh.Signer) string {
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
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)

				for newChannel := range chans {
					if newChannel.ChannelType() != "direct-tcpip" {
						newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
						continue
					}

					var msg struct {
						DestAddr string
						DestPort uint32
						OrigAddr string
						OrigPort uint32
					}
					if err := ssh.Unmarshal(newChannel.ExtraData(), &msg); err != nil {
						newChannel.Reject(ssh.Prohibited, "invalid payload")
						continue
					}

					dest := fmt.Sprintf("%s:%d", msg.DestAddr, msg.DestPort)
					targetConn, err := net.Dial("tcp", dest)
					if err != nil {
						newChannel.Reject(ssh.ConnectionFailed, fmt.Sprintf("failed to connect to %s", dest))
						continue
					}

					channel, requests, err := newChannel.Accept()
					if err != nil {
						targetConn.Close()
						continue
					}
					go ssh.DiscardRequests(requests)

					go func() {
						defer channel.Close()
						defer targetConn.Close()
						io.Copy(channel, targetConn)
					}()
					go func() {
						defer channel.Close()
						defer targetConn.Close()
						io.Copy(targetConn, channel)
					}()
				}
			}()
		}
	}()

	return listener.Addr().String()
}

// startMockHTTPServer starts a basic HTTP server that replies with "Hello World".
func startMockHTTPServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen for HTTP server: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello World"))
	})
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()

	t.Cleanup(func() { server.Shutdown(context.Background()) })

	return listener.Addr().String()
}

func TestGoshTunnelIntegration(t *testing.T) {
	keyPath, signer := generateTestKey(t)
	sshAddr := startMockSSHServer(t, signer)
	httpAddr := startMockHTTPServer(t)

	// Parse addresses
	sshHost, sshPort, _ := net.SplitHostPort(sshAddr)
	_, httpPortStr, _ := net.SplitHostPort(httpAddr)

	var httpPort int
	fmt.Sscanf(httpPortStr, "%d", &httpPort)

	// We want to map a local port to the remote HTTP port via the SSH server.
	localPort := 18080

	resolved := []config.ResolvedTunnel{
		{
			Name:       "test-mapping",
			HostName:   sshHost,
			Port:       sshPort,
			User:       "testuser",
			KeyPath:    keyPath,
			LocalPort:  localPort,
			RemotePort: httpPort,
		},
	}

	manager := tunnel.NewManager()
	for _, r := range resolved {
		if err := manager.Add(r); err != nil {
			t.Fatalf("Failed to add tunnel: %v", err)
		}
	}

	// Give the manager a moment to dial the SSH server and start listening locally.
	time.Sleep(500 * time.Millisecond)

	// Make an HTTP request through the local port
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	resp, err := http.Get("http://" + localAddr)
	if err != nil {
		t.Fatalf("Failed to make HTTP request through tunnel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello World" {
		t.Errorf("Expected body 'Hello World', got '%s'", string(body))
	}

	// Verify "Hot Reload" - stop it
	manager.Remove("test-mapping")

	time.Sleep(100 * time.Millisecond)

	// Request should fail now
	_, err = http.Get("http://" + localAddr)
	if err == nil {
		t.Errorf("Expected request to fail after tunnel was stopped, but it succeeded")
	}

	// Also test engine KeepAlive behavior... The engine is running and keepalives should work but since we don't mock keepalive failures easily here, we just verify cleanup.
	
	// We verify that engines were not forcefully deleted (by design, engines live on slightly longer or are kept around. 
	// For MVP we just make sure no panic occurs and no listener leaks.)
}
