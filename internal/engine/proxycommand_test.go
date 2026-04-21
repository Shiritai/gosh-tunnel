package engine_test

import (
	"fmt"
	"net"
	"os/exec"
	"testing"

	"gosh-tunnel/internal/engine"
)

// TestEngineConnectViaProxyCommand verifies the SSH handshake flows over
// stdio of a child ProxyCommand process. We use `nc` to bridge stdio<->TCP
// to a real mock SSH server listening on a random local port.
func TestEngineConnectViaProxyCommand(t *testing.T) {
	ncPath, err := exec.LookPath("nc")
	if err != nil {
		t.Skip("nc not available, skipping ProxyCommand integration test")
	}

	keyPath, signer := generateTestKey(t)
	addr, listener := startMockSSHServer(t, signer)
	defer listener.Close()

	_, port, _ := net.SplitHostPort(addr)
	proxyCmd := fmt.Sprintf("%s 127.0.0.1 %s", ncPath, port)

	eng := engine.NewWithProxy("placeholder.invalid", "22", "testuser", keyPath, proxyCmd)

	client, err := eng.GetClient()
	if err != nil {
		t.Fatalf("GetClient via ProxyCommand failed: %v", err)
	}
	if client == nil {
		t.Fatalf("nil client returned")
	}

	if _, _, err := client.SendRequest("keepalive@gosh.tunnel", true, nil); err != nil {
		t.Fatalf("keepalive over proxied SSH failed: %v", err)
	}

	client.Close()
}

func TestEngineProxyCommandFailure(t *testing.T) {
	keyPath, _ := generateTestKey(t)
	// Command exits immediately with no output -> SSH handshake should fail.
	eng := engine.NewWithProxy("placeholder.invalid", "22", "testuser", keyPath, "true")
	if _, err := eng.GetClient(); err == nil {
		t.Fatalf("expected handshake failure when ProxyCommand exits immediately")
	}
}
