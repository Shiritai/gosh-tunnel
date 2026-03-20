package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"gosh-tunnel/internal/config"
)

func TestLoadConfigErrors(t *testing.T) {
	// 1. File not found
	_, err := config.LoadConfig("nonexistent.yaml")
	if err == nil {
		t.Fatal("Expected error when loading nonexistent file, got nil")
	}

	// 2. Malformed YAML
	tmpDir := t.TempDir()
	malformedPath := filepath.Join(tmpDir, "bad.yaml")
	os.WriteFile(malformedPath, []byte("server: \n\tbad_indentation: val"), 0644)
	
	_, err = config.LoadConfig(malformedPath)
	if err == nil {
		t.Fatal("Expected error when loading malformed yaml, got nil")
	}
}

func TestExpandPortsErrors(t *testing.T) {
	cfg := &config.ConfigFile{
		Tunnels: []config.TunnelConfig{
			{
				Server: "testsvr",
				Ports:  []string{"invalid-port-format"},
			},
		},
	}
	
	tmpDir := t.TempDir()
	sshConfigPath := filepath.Join(tmpDir, "config")
	os.WriteFile(sshConfigPath, []byte("Host testsvr\n"), 0644)
	cfg.SSHConfigPath = sshConfigPath

	_, err := config.ResolveTunnels(cfg)
	if err == nil {
		t.Fatal("Expected error when resolving invalid port mappings, got nil")
	}
}
