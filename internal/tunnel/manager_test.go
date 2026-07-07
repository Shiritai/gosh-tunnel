package tunnel_test

import (
	"fmt"
	"testing"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/tunnel"
)

func mkTunnel(server string, local, remote int) config.ResolvedTunnel {
	return config.ResolvedTunnel{
		Name:       fmt.Sprintf("%s-%d:%d", server, local, remote),
		Server:     server,
		HostName:   "127.0.0.1",
		Port:       "22",
		LocalPort:  local,
		RemotePort: remote,
	}
}

func TestRemoveReturnsConfig(t *testing.T) {
	mgr := tunnel.NewManager()
	if err := mgr.Add(mkTunnel("alpha", 25801, 80)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	rt, err := mgr.Remove("alpha-25801:80")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if rt.Server != "alpha" || rt.LocalPort != 25801 || rt.RemotePort != 80 {
		t.Errorf("Remove returned wrong config: %+v", rt)
	}
	if len(mgr.Status()) != 0 {
		t.Errorf("Expected no tunnels after remove, got %v", mgr.Status())
	}
}

func TestRemoveByLocalPort(t *testing.T) {
	mgr := tunnel.NewManager()
	if err := mgr.Add(mkTunnel("alpha", 25811, 80)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := mgr.Add(mkTunnel("beta", 25812, 90)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	rt, err := mgr.RemoveByLocalPort(25811)
	if err != nil {
		t.Fatalf("RemoveByLocalPort failed: %v", err)
	}
	if rt.Server != "alpha" || rt.RemotePort != 80 {
		t.Errorf("Removed wrong tunnel: %+v", rt)
	}

	left := mgr.Status()
	if len(left) != 1 || left[0] != "beta-25812:90" {
		t.Errorf("Expected only beta tunnel left, got %v", left)
	}
}

func TestRemoveByLocalPortNotFound(t *testing.T) {
	mgr := tunnel.NewManager()
	if _, err := mgr.RemoveByLocalPort(25899); err == nil {
		t.Fatal("Expected error for unknown local port, got nil")
	}
}

func TestRemoveByServer(t *testing.T) {
	mgr := tunnel.NewManager()
	for _, rt := range []config.ResolvedTunnel{
		mkTunnel("alpha", 25821, 80),
		mkTunnel("alpha", 25822, 81),
		mkTunnel("beta", 25823, 90),
	} {
		if err := mgr.Add(rt); err != nil {
			t.Fatalf("Add %s failed: %v", rt.Name, err)
		}
	}

	removed, err := mgr.RemoveByServer("alpha")
	if err != nil {
		t.Fatalf("RemoveByServer failed: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("Expected 2 removed tunnels, got %d: %+v", len(removed), removed)
	}
	for _, rt := range removed {
		if rt.Server != "alpha" {
			t.Errorf("Removed tunnel from wrong server: %+v", rt)
		}
	}

	left := mgr.Status()
	if len(left) != 1 || left[0] != "beta-25823:90" {
		t.Errorf("Expected only beta tunnel left, got %v", left)
	}

	if _, err := mgr.RemoveByServer("gamma"); err == nil {
		t.Error("Expected error for unknown server, got nil")
	}
}

func TestListSorted(t *testing.T) {
	mgr := tunnel.NewManager()
	for _, rt := range []config.ResolvedTunnel{
		mkTunnel("beta", 25831, 90),
		mkTunnel("alpha", 25833, 81),
		mkTunnel("alpha", 25832, 80),
	} {
		if err := mgr.Add(rt); err != nil {
			t.Fatalf("Add %s failed: %v", rt.Name, err)
		}
	}

	list := mgr.List()
	if len(list) != 3 {
		t.Fatalf("Expected 3 tunnels, got %d", len(list))
	}
	wantOrder := []int{25832, 25833, 25831}
	for i, want := range wantOrder {
		if list[i].LocalPort != want {
			t.Errorf("List order wrong at %d: want local %d, got %+v", i, want, list[i])
		}
	}
}
