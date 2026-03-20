package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRange(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
		err      bool
	}{
		{"8080", []int{8080}, false},
		{"8080-8082", []int{8080, 8081, 8082}, false},
		{"invalid", nil, true},
		{"8080-8079", nil, true}, // start > end
	}

	for _, tc := range tests {
		res, err := parseRange(tc.input)
		if (err != nil) != tc.err {
			t.Errorf("parseRange(%s) unexpected error: %v", tc.input, err)
		}
		if !tc.err {
			if len(res) != len(tc.expected) {
				t.Errorf("parseRange(%s) len: got %d, want %d", tc.input, len(res), len(tc.expected))
			}
			for i, v := range res {
				if v != tc.expected[i] {
					t.Errorf("parseRange(%s)[%d]: got %d, want %d", tc.input, i, v, tc.expected[i])
				}
			}
		}
	}
}

func TestExpandPorts(t *testing.T) {
	tests := []struct {
		input    string
		expected []portMapping
		err      bool
	}{
		{"8080:80", []portMapping{{8080, 80}}, false},
		{"3000-3002:4000-4002", []portMapping{{3000, 4000}, {3001, 4001}, {3002, 4002}}, false},
		{"3000-3002:80", []portMapping{{3000, 80}, {3001, 80}, {3002, 80}}, false},
		{"80:3000-3002", []portMapping{{80, 3000}, {80, 3001}, {80, 3002}}, false},
		{"3000-3001:4000-4002", nil, true}, // mismatched length
		{"invalid:80", nil, true},
	}

	for _, tc := range tests {
		res, err := expandPorts(tc.input)
		if (err != nil) != tc.err {
			t.Errorf("expandPorts(%s) unexpected error: %v", tc.input, err)
		}
		if !tc.err {
			if len(res) != len(tc.expected) {
				t.Errorf("expandPorts(%s) len: got %d, want %d", tc.input, len(res), len(tc.expected))
			}
			for i, v := range res {
				if v.local != tc.expected[i].local || v.remote != tc.expected[i].remote {
					t.Errorf("expandPorts(%s)[%d]: got %v, want %v", tc.input, i, v, tc.expected[i])
				}
			}
		}
	}
}

func TestResolveTunnels(t *testing.T) {
	// Create a dummy ssh_config
	tmpDir := t.TempDir()
	sshConfigPath := filepath.Join(tmpDir, "config")
	err := os.WriteFile(sshConfigPath, []byte(`
Host lab-server
  Hostname 192.168.1.100
  User testuser
  Port 2222
  IdentityFile ~/.ssh/test_rsa
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ConfigFile{
		SSHConfigPath: sshConfigPath,
		Tunnels: []TunnelConfig{
			{
				Server: "lab-server",
				Ports:  []string{"8080:80", "9000-9001:9000-9001"},
			},
		},
	}

	resolved, err := ResolveTunnels(cfg)
	if err != nil {
		t.Fatalf("ResolveTunnels failed: %v", err)
	}

	if len(resolved) != 3 {
		t.Fatalf("expected 3 resolved tunnels, got %d", len(resolved))
	}

	if resolved[0].HostName != "192.168.1.100" || resolved[0].User != "testuser" || resolved[0].Port != "2222" {
		t.Errorf("ssh config not resolved correctly, got: %+v", resolved[0])
	}
	
	if resolved[0].LocalPort != 8080 || resolved[0].RemotePort != 80 {
		t.Errorf("port parse mismatch, got %d:%d", resolved[0].LocalPort, resolved[0].RemotePort)
	}
}
func TestConfigPersistence(t *testing.T) {
	cfg := &ConfigFile{
		Tunnels: []TunnelConfig{
			{
				Server: "server1",
				Ports:  []string{"80:80"},
			},
		},
	}

	// 1. Test Add
	AddTunnelToConfig(cfg, "server1", "90:90")
	if len(cfg.Tunnels[0].Ports) != 2 || cfg.Tunnels[0].Ports[1] != "90:90" {
		t.Errorf("AddTunnelToConfig failed to append port, ports: %v", cfg.Tunnels[0].Ports)
	}

	AddTunnelToConfig(cfg, "server2", "22:22")
	if len(cfg.Tunnels) != 2 || cfg.Tunnels[1].Server != "server2" {
		t.Errorf("AddTunnelToConfig failed to add new server")
	}

	// 2. Test Remove
	removed := RemoveTunnelTargetFromConfig(cfg, "server1", "80:80")
	if !removed || len(cfg.Tunnels[0].Ports) != 1 || cfg.Tunnels[0].Ports[0] != "90:90" {
		t.Errorf("RemoveTunnelTargetFromConfig failed to remove port mapping")
	}

	removed = RemoveTunnelTargetFromConfig(cfg, "server1", "non-existent")
	if removed {
		t.Errorf("RemoveTunnelTargetFromConfig reported removal for non-existent mapping")
	}
}
