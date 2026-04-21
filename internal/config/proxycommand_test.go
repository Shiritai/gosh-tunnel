package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandProxyCommandTokens(t *testing.T) {
	tests := []struct {
		name               string
		cmd, h, p, u, want string
	}{
		{"basic", "proxy --hostname %h", "host.example.test", "22", "alice",
			"proxy --hostname host.example.test"},
		{"all tokens", "ssh -W %h:%p -l %r jump", "h.example.test", "2222", "bob",
			"ssh -W h.example.test:2222 -l bob jump"},
		{"escaped percent", "echo 100%% done %h", "h", "p", "u", "echo 100% done h"},
		{"empty", "", "h", "p", "u", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandProxyCommandTokens(tc.cmd, tc.h, tc.p, tc.u)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveTunnelsWithProxyCommand(t *testing.T) {
	tmp := t.TempDir()
	sshCfgPath := filepath.Join(tmp, "ssh_config")
	sshCfg := `Host proxied-host
    HostName proxied.example.test
    User alice
    ProxyCommand fake-proxy --hostname %h

Host plain-host
    HostName plain.example.test
    User bob
    Port 2222
`
	if err := os.WriteFile(sshCfgPath, []byte(sshCfg), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &ConfigFile{
		SSHConfigPath: sshCfgPath,
		Tunnels: []TunnelConfig{
			{Server: "proxied-host", Ports: []string{"9000:9000"}},
			{Server: "plain-host", Ports: []string{"8080:80"}},
		},
	}

	resolved, err := ResolveTunnels(cfg)
	if err != nil {
		t.Fatalf("ResolveTunnels failed: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(resolved))
	}

	var proxied, plain *ResolvedTunnel
	for i := range resolved {
		switch resolved[i].HostName {
		case "proxied.example.test":
			proxied = &resolved[i]
		case "plain.example.test":
			plain = &resolved[i]
		}
	}
	if proxied == nil || plain == nil {
		t.Fatalf("missing expected hosts: %+v", resolved)
	}

	wantProxy := "fake-proxy --hostname proxied.example.test"
	if proxied.ProxyCommand != wantProxy {
		t.Errorf("proxied ProxyCommand: got %q, want %q", proxied.ProxyCommand, wantProxy)
	}
	if proxied.User != "alice" {
		t.Errorf("proxied user: got %q", proxied.User)
	}
	if plain.ProxyCommand != "" {
		t.Errorf("plain ProxyCommand should be empty, got %q", plain.ProxyCommand)
	}
	if plain.Port != "2222" {
		t.Errorf("plain port: got %q", plain.Port)
	}
}
