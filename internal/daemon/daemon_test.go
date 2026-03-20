package daemon_test

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/daemon"
	"gosh-tunnel/internal/tunnel"
)

func TestDaemonIPCAddRemoveFlow(t *testing.T) {
	// 1. Setup Manager and Daemon
	mgr := tunnel.NewManager()
	
	// Create a custom socket just for testing
	testSocket := fmt.Sprintf("/tmp/gosh-test-%d.sock", time.Now().UnixNano())
	daemon.SocketPath = testSocket
	defer os.Remove(testSocket)

	srv := daemon.NewServer(mgr)
	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer srv.Stop()

	// 2. Client Side
	cli := daemon.NewClient()

	// 3. Test empty status
	status, err := cli.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if len(status) != 0 {
		t.Errorf("Expected 0 active tunnels, got %d", len(status))
	}

	// 4. Test Add
	tunnConf := config.ResolvedTunnel{
		Name:      "test-tunnel-8080",
		HostName:  "127.0.0.1",
		Port:      "22", // fake
		LocalPort: 18080,
		RemotePort: 80,
	}

	err = cli.Add(tunnConf)
	if err != nil {
		t.Errorf("Add failed: %v", err)
	}

	// 5. Test Status after Add
	status, err = cli.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if len(status) != 1 || status[0] != "test-tunnel-8080" {
		t.Errorf("Expected status to contain [test-tunnel-8080], got %v", status)
	}

	// 6. Test Remove
	err = cli.Remove("test-tunnel-8080")
	if err != nil {
		t.Errorf("Remove failed: %v", err)
	}

	// 7. Verify removed
	status, _ = cli.Status()
	if len(status) != 0 {
		t.Errorf("Expected tunnel to be removed, got: %v", status)
	}
}

// TestDaemonIPCFuzz simulates a highly concurrent environment where multiple clients
// furiously send add and remove commands at the exact same time through the unix socket.
func TestDaemonIPCFuzz(t *testing.T) {
	mgr := tunnel.NewManager()
	
	testSocket := fmt.Sprintf("/tmp/gosh-fuzz-%d.sock", time.Now().UnixNano())
	daemon.SocketPath = testSocket
	defer os.Remove(testSocket)

	srv := daemon.NewServer(mgr)
	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer srv.Stop()

	clientCount := 30
	var wg sync.WaitGroup

	for i := 0; i < clientCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			cli := daemon.NewClient()
			
			// Each worker attempts 50 random ops
			for j := 0; j < 50; j++ {
				port := 20000 + rand.Intn(100) // 100 possible ports
				name := fmt.Sprintf("fuzz-host-local:%d", port)

				if rand.Float32() < 0.6 {
					// Add
					_ = cli.Add(config.ResolvedTunnel{
						Name:       name,
						HostName:   "127.0.0.1",
						Port:       "22",
						User:       "fuzz",
						LocalPort:  port,
						RemotePort: 80,
					})
				} else {
					// Remove
					_ = cli.Remove(name)
				}
				// Occasionally check status just to spam the socket
				if rand.Float32() < 0.1 {
					_, _ = cli.Status()
				}
			}
		}(i)
	}

	wg.Wait()
	
	// Ensure server hasn't crashed and can answer a final status
	cli := daemon.NewClient()
	_, err := cli.Status()
	if err != nil {
		t.Fatalf("Daemon seemingly crashed or hung after fuzzing: %v", err)
	}
}
// TestDaemonIPCResilience ensures the server doesn't crash on invalid input or connection resets
func TestDaemonIPCResilience(t *testing.T) {
	mgr := tunnel.NewManager()
	testSocket := fmt.Sprintf("/tmp/gosh-resilience-%d.sock", time.Now().UnixNano())
	daemon.SocketPath = testSocket
	defer os.Remove(testSocket)

	srv := daemon.NewServer(mgr)
	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer srv.Stop()

	// 1. Send garbage data
	conn, err := net.Dial("unix", testSocket)
	if err == nil {
		fmt.Fprintf(conn, "GARBAGE-DATA-NOT-JSON")
		conn.Close()
	}

	// 2. Immediate disconnect
	conn, err = net.Dial("unix", testSocket)
	if err == nil {
		conn.Close()
	}

	// 3. Verify daemon still works
	cli := daemon.NewClient()
	_, err = cli.Status()
	if err != nil {
		t.Errorf("Daemon should have survived garbage data, but got error: %v", err)
	}
}
