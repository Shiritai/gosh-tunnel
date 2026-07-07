package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
	"gopkg.in/yaml.v3"
)

// Validators for values substituted into a ProxyCommand template.
// They reject anything that could break out of the token and inject shell.
var (
	validHostRe = regexp.MustCompile(`^[A-Za-z0-9._\-:\[\]]+$`)
	validPortRe = regexp.MustCompile(`^[0-9]{1,5}$`)
	validUserRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._\-]*$`)
)

// ValidateProxyCommandTokenValues ensures host/port/user are safe to substitute
// into a `sh -c <cmd>` ProxyCommand. It rejects values containing shell
// metacharacters or other unexpected bytes that could enable command injection.
func ValidateProxyCommandTokenValues(host, port, user string) error {
	if !validHostRe.MatchString(host) {
		return fmt.Errorf("unsafe host %q for ProxyCommand substitution", host)
	}
	if !validPortRe.MatchString(port) {
		return fmt.Errorf("unsafe port %q for ProxyCommand substitution", port)
	}
	if user != "" && !validUserRe.MatchString(user) {
		return fmt.Errorf("unsafe user %q for ProxyCommand substitution", user)
	}
	return nil
}

type TunnelConfig struct {
	Server     string   `yaml:"server"`
	RemoteHost string   `yaml:"remote_host,omitempty"`
	Ports      []string `yaml:"ports"`
}

// DefaultRemoteHost is what we dial on the remote side when the user does not
// specify remote_host. We use "localhost" (not "127.0.0.1") so the remote sshd
// performs resolution and tries both IPv4 and IPv6 — matching `ssh -L` and
// avoiding "connection refused" when the target (e.g. Vite dev server) binds
// only to ::1.
const DefaultRemoteHost = "localhost"

type ConfigFile struct {
	SSHConfigPath string         `yaml:"ssh_config"`
	Tunnels       []TunnelConfig `yaml:"tunnels"`
}

// ResolvedTunnel represents a single port mapping after expanding port ranges.
// Server keeps the user-facing alias so callers never have to parse it back
// out of Name.
type ResolvedTunnel struct {
	Name         string
	Server       string
	HostName     string
	Port         string
	User         string
	KeyPath      string
	ProxyCommand string
	LocalPort    int
	RemoteHost   string
	RemotePort   int
}

// PortMapping returns the "local:remote" string used in config files.
func (t ResolvedTunnel) PortMapping() string {
	return fmt.Sprintf("%d:%d", t.LocalPort, t.RemotePort)
}

// ExpandProxyCommandTokens expands OpenSSH ProxyCommand tokens (%h, %p, %r, %%).
func ExpandProxyCommandTokens(cmd, host, port, user string) string {
	first := strings.NewReplacer("%%", "\x00", "%h", host, "%p", port, "%r", user).Replace(cmd)
	return strings.ReplaceAll(first, "\x00", "%")
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

// ServerSettings holds the per-server fields resolved from ~/.ssh/config.
type ServerSettings struct {
	HostName     string
	Port         string
	User         string
	KeyPath      string
	ProxyCommand string
}

// ResolveServer looks up a single Host alias in the given ssh_config file and
// returns the effective connection settings, applying the same defaults and
// ProxyCommand token expansion as ResolveTunnels. If sshConfigPath cannot be
// opened the alias is returned as-is with defaults (User=$USER, Port=22), so
// callers still work when the user has no ssh_config.
func ResolveServer(sshConfigPath, alias string) (ServerSettings, error) {
	s := ServerSettings{
		HostName: alias,
		Port:     "22",
		User:     os.Getenv("USER"),
	}

	f, err := os.Open(sshConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("failed to open ssh config: %w", err)
	}
	defer f.Close()

	sshCfg, err := ssh_config.Decode(f)
	if err != nil {
		return s, fmt.Errorf("failed to decode ssh config: %w", err)
	}

	if hn, err := sshCfg.Get(alias, "Hostname"); err == nil && hn != "" {
		s.HostName = hn
	}
	if u, _ := sshCfg.Get(alias, "User"); u != "" {
		s.User = u
	}
	if p, _ := sshCfg.Get(alias, "Port"); p != "" {
		s.Port = p
	}
	if kp, _ := sshCfg.Get(alias, "IdentityFile"); kp != "" {
		if strings.HasPrefix(kp, "~/") {
			home, _ := os.UserHomeDir()
			kp = filepath.Join(home, kp[2:])
		}
		s.KeyPath = kp
	}
	if pc, _ := sshCfg.Get(alias, "ProxyCommand"); pc != "" {
		if err := ValidateProxyCommandTokenValues(s.HostName, s.Port, s.User); err != nil {
			return s, fmt.Errorf("server %q: %w", alias, err)
		}
		s.ProxyCommand = ExpandProxyCommandTokens(pc, s.HostName, s.Port, s.User)
	}
	return s, nil
}

// DefaultSSHConfigPath returns ~/.ssh/config.
func DefaultSSHConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

// ResolveTunnels parses the ssh_config and expands the ranges into individual tunnels.
func ResolveTunnels(cfg *ConfigFile) ([]ResolvedTunnel, error) {
	var resolved []ResolvedTunnel
	for _, t := range cfg.Tunnels {
		s, err := ResolveServer(cfg.SSHConfigPath, t.Server)
		if err != nil {
			return nil, err
		}
		hostName := s.HostName
		port := s.Port
		user := s.User
		keyPath := s.KeyPath
		proxyCommand := s.ProxyCommand

		remoteHost := t.RemoteHost
		if remoteHost == "" {
			remoteHost = DefaultRemoteHost
		}

		for _, p := range t.Ports {
			mappings, err := expandPorts(p)
			if err != nil {
				return nil, fmt.Errorf("failed to parse port mapping '%s': %w", p, err)
			}

			for _, m := range mappings {
				resolved = append(resolved, ResolvedTunnel{
					Name:         fmt.Sprintf("%s-%d:%d", t.Server, m.local, m.remote),
					Server:       t.Server,
					HostName:     hostName,
					Port:         port,
					User:         user,
					KeyPath:      keyPath,
					ProxyCommand: proxyCommand,
					LocalPort:    m.local,
					RemoteHost:   remoteHost,
					RemotePort:   m.remote,
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
// remoteHost is only recorded when creating a new server entry, and only when
// it is non-default; existing entries are left untouched to avoid clobbering
// user-edited config.
func AddTunnelToConfig(cfg *ConfigFile, server string, portMapping string, remoteHost string) {
	for i, t := range cfg.Tunnels {
		if t.Server == server {
			for _, p := range t.Ports {
				if p == portMapping {
					return
				}
			}
			cfg.Tunnels[i].Ports = append(cfg.Tunnels[i].Ports, portMapping)
			return
		}
	}

	entry := TunnelConfig{
		Server: server,
		Ports:  []string{portMapping},
	}
	if remoteHost != "" && remoteHost != DefaultRemoteHost {
		entry.RemoteHost = remoteHost
	}
	cfg.Tunnels = append(cfg.Tunnels, entry)
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

