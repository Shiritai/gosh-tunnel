package tunnel_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/daemon"
	"gosh-tunnel/internal/tunnel"
)

// TestRealWorldScenario simulates the exact use case provided by the user:
// 1. Initial configuration of 2 servers and 8 ports total
// 2. Establishing them all via the Daemon IPC
// 3. Modifying (hot-reloading) by dropping one and adding a new target
// 4. Checking status at all phases to ensure correctness
func TestRealWorldScenario(t *testing.T) {
	// 1. Setup Mock Servers
	keyPath, signer := generateTestKey(t)
	mockSshServerAddr := startMockSSHServer(t, signer)
	mockHttpServer1 := startMockHTTPServer(t)
	mockHttpServer2 := startMockHTTPServer(t)

	// Since we only have one mock SSH server here, we pretend it's both 
	// `gpu-server` and `workstation` by parsing its dynamic address.
	sshHost, sshPort, _ := net.SplitHostPort(mockSshServerAddr)
	_, httpPort1, _ := net.SplitHostPort(mockHttpServer1)
	_, httpPort2, _ := net.SplitHostPort(mockHttpServer2)

	// 2. Setup Gosh Daemon
	mgr := tunnel.NewManager()
	
	testSocket := fmt.Sprintf("/tmp/gosh-realworld-%d.sock", time.Now().UnixNano())
	daemon.SocketPath = testSocket
	defer os.Remove(testSocket)

	srv := daemon.NewServer(mgr)
	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer srv.Stop()
	cli := daemon.NewClient()

	// 3. User powers on laptop - Emulate 8 total ports setup via config/IPC
	// Instead of standard ports that might clash (8080), we pick high random block
	basePortLab := 42770 // 42770-42774 (5 ports)
	basePortHome := 42990 // 42990-42992 (3 ports)

	// Add 5 ports for gpu-server
	for i := 0; i <= 4; i++ {
		tunn := config.ResolvedTunnel{
			Name:       fmt.Sprintf("gpu-server-%d", basePortLab+i),
			HostName:   sshHost,
			Port:       sshPort,
			User:       "user",
			KeyPath:    keyPath,
			LocalPort:  basePortLab + i,
			RemotePort: parseInt(httpPort1), // Forward to our mock HTTP 1
		}
		if err := cli.Add(tunn); err != nil {
			t.Fatalf("Failed adding lab port %d: %v", basePortLab+i, err)
		}
	}

	// Add 3 ports for workstation
	for i := 0; i <= 2; i++ {
		tunn := config.ResolvedTunnel{
			Name:       fmt.Sprintf("workstation-%d", basePortHome+i),
			HostName:   sshHost,
			Port:       sshPort,
			User:       "user",
			KeyPath:    keyPath,
			LocalPort:  basePortHome + i,
			RemotePort: parseInt(httpPort2), // Forward to our mock HTTP 2
		}
		if err := cli.Add(tunn); err != nil {
			t.Fatalf("Failed adding home port %d: %v", basePortHome+i, err)
		}
	}

	// 4. Verify all 8 ports are listening and routing successfully
	time.Sleep(500 * time.Millisecond) // Let SSH handshake
	status, _ := cli.Status()
	if len(status) != 8 {
		t.Fatalf("Expected 8 active tunnels, got %d", len(status))
	}

	// Verify lab traffic on last port
	verifyTraffic(t, basePortLab+4, "Hello World")
	// Verify home traffic on first port
	verifyTraffic(t, basePortHome, "Hello World")

	// 5. User Hot-Reload Operation: "Remove home ports and add a new temporary DB port"
	for i := 0; i <= 2; i++ {
		if err := cli.Remove(fmt.Sprintf("workstation-%d", basePortHome+i)); err != nil {
			t.Errorf("Failed to remove home port %d: %v", basePortHome+i, err)
		}
	}

	tempDbPort := 43000
	err := cli.Add(config.ResolvedTunnel{
		Name:       "temp-db",
		HostName:   sshHost,
		Port:       sshPort,
		User:       "user",
		KeyPath:    keyPath,
		LocalPort:  tempDbPort,
		RemotePort: parseInt(httpPort1),
	})
	if err != nil {
		t.Fatalf("Failed adding temp-db: %v", err)
	}

	// 6. Verify post-hot-reload state
	time.Sleep(100 * time.Millisecond)
	status, _ = cli.Status()
	if len(status) != 6 { // 5 lab + 1 temp-db
		t.Fatalf("Post hot-reload: Expected 6 active tunnels, got %d", len(status))
	}

	// Old port should be dead
	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d", basePortHome))
	if err == nil {
		t.Errorf("Expected connection to removed port %d to fail, but it succeeded", basePortHome)
	}

	// New port should route
	verifyTraffic(t, tempDbPort, "Hello World")
}

func parseInt(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

func verifyTraffic(t *testing.T, localPort int, expectedBody string) {
	t.Helper()
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	resp, err := http.Get("http://" + localAddr)
	if err != nil {
		t.Fatalf("Failed to make HTTP request on port %d: %v", localPort, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Port %d: Expected status 200, got %d", localPort, resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != expectedBody {
		t.Errorf("Port %d: Expected body '%s', got '%s'", localPort, expectedBody, string(body))
	}
}
