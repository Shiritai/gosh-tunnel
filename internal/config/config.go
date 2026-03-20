package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
	"gopkg.in/yaml.v3"
)

type TunnelConfig struct {
	Server string   `yaml:"server"`
	Ports  []string `yaml:"ports"`
}

type ConfigFile struct {
	SSHConfigPath string         `yaml:"ssh_config"`
	Tunnels       []TunnelConfig `yaml:"tunnels"`
}

// ResolvedTunnel represents a single port mapping after expanding port ranges
type ResolvedTunnel struct {
	Name       string
	HostName   string
	Port       string
	User       string
	KeyPath    string
	LocalPort  int
	RemotePort int
}

// LoadConfig reads the YAML configuration file.
func LoadConfig(path string) (*ConfigFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg ConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml: %w", err)
	}

	if cfg.SSHConfigPath == "" {
		home, _ := os.UserHomeDir()
		cfg.SSHConfigPath = filepath.Join(home, ".ssh", "config")
	} else if strings.HasPrefix(cfg.SSHConfigPath, "~/") {
		home, _ := os.UserHomeDir()
		cfg.SSHConfigPath = filepath.Join(home, cfg.SSHConfigPath[2:])
	}

	return &cfg, nil
}

// ResolveTunnels parses the ssh_config and expands the ranges into individual tunnels.
func ResolveTunnels(cfg *ConfigFile) ([]ResolvedTunnel, error) {
	f, err := os.Open(cfg.SSHConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open ssh config: %w", err)
	}
	defer f.Close()

	sshCfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ssh config: %w", err)
	}

	var resolved []ResolvedTunnel
	for _, t := range cfg.Tunnels {
		hostName, err := sshCfg.Get(t.Server, "Hostname")
		if err != nil || hostName == "" {
			hostName = t.Server // Default to server name if not found
		}

		user, _ := sshCfg.Get(t.Server, "User")
		if user == "" {
			user = os.Getenv("USER")
		}

		port, _ := sshCfg.Get(t.Server, "Port")
		if port == "" {
			port = "22"
		}

		keyPath, _ := sshCfg.Get(t.Server, "IdentityFile")
		if strings.HasPrefix(keyPath, "~/") {
			home, _ := os.UserHomeDir()
			keyPath = filepath.Join(home, keyPath[2:])
		}

		for _, p := range t.Ports {
			mappings, err := expandPorts(p)
			if err != nil {
				return nil, fmt.Errorf("failed to parse port mapping '%s': %w", p, err)
			}

			for _, m := range mappings {
				resolved = append(resolved, ResolvedTunnel{
					Name:       fmt.Sprintf("%s-%d:%d", t.Server, m.local, m.remote),
					HostName:   hostName,
					Port:       port,
					User:       user,
					KeyPath:    keyPath,
					LocalPort:  m.local,
					RemotePort: m.remote,
				})
			}
		}
	}

	return resolved, nil
}

type portMapping struct {
	local  int
	remote int
}

func expandPorts(mapping string) ([]portMapping, error) {
	parts := strings.Split(mapping, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid mapping format, expected local:remote")
	}

	localRange, err := parseRange(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid local port: %w", err)
	}

	remoteRange, err := parseRange(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid remote port: %w", err)
	}

	if len(localRange) != len(remoteRange) && len(localRange) != 1 && len(remoteRange) != 1 {
		return nil, fmt.Errorf("port range length mismatch")
	}

	var mappings []portMapping
	maxLen := len(localRange)
	if len(remoteRange) > maxLen {
		maxLen = len(remoteRange)
	}

	for i := 0; i < maxLen; i++ {
		l := localRange[0]
		if i < len(localRange) {
			l = localRange[i]
		}
		r := remoteRange[0]
		if i < len(remoteRange) {
			r = remoteRange[i]
		}
		mappings = append(mappings, portMapping{local: l, remote: r})
	}

	return mappings, nil
}

// SaveConfig writes the ConfigFile back to the YAML file.
func SaveConfig(path string, cfg *ConfigFile) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// AddTunnelToConfig adds or updates a tunnel entry in the ConfigFile struct.
func AddTunnelToConfig(cfg *ConfigFile, server string, portMapping string) {
	for i, t := range cfg.Tunnels {
		if t.Server == server {
			// Check if mapping already exists
			for _, p := range t.Ports {
				if p == portMapping {
					return
				}
			}
			cfg.Tunnels[i].Ports = append(cfg.Tunnels[i].Ports, portMapping)
			return
		}
	}

	// New server entry
	cfg.Tunnels = append(cfg.Tunnels, TunnelConfig{
		Server: server,
		Ports:  []string{portMapping},
	})
}

// RemoveTunnelTargetFromConfig removes a specific port mapping string from the ConfigFile struct.
// It returns true if anything was removed.
func RemoveTunnelTargetFromConfig(cfg *ConfigFile, server string, portMapping string) bool {
	removed := false
	for i, t := range cfg.Tunnels {
		if t.Server == server {
			var newPorts []string
			for _, p := range t.Ports {
				if p != portMapping {
					newPorts = append(newPorts, p)
				} else {
					removed = true
				}
			}
			cfg.Tunnels[i].Ports = newPorts
			
			// If no ports left, we keep the server entry but it's empty.
			// Alternatively we could remove it, but keeping it is safer.
			return removed
		}
	}
	return removed
}

func parseRange(r string) ([]int, error) {
	if !strings.Contains(r, "-") {
		p, err := strconv.Atoi(r)
		if err != nil {
			return nil, err
		}
		return []int{p}, nil
	}

	parts := strings.Split(r, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range format")
	}

	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, err
	}

	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, err
	}

	if start > end {
		return nil, fmt.Errorf("start port greater than end port")
	}

	var res []int
	for i := start; i <= end; i++ {
		res = append(res, i)
	}
	return res, nil
}

