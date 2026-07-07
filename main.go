package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/daemon"
	"gosh-tunnel/internal/tunnel"
)

const shutdownTimeout = 5 * time.Second

var rootCmd = &cobra.Command{
	Use:   "gosh",
	Short: "Gosh Configurable SSH Tunnel",
	Long:  `A High-Performance SSH Tunnel Manager.`,
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the gosh daemon",
	Run: func(cmd *cobra.Command, args []string) {
		cfgPath, _ := cmd.Flags().GetString("config")
		log.Println("Starting Gosh-Tunnel Daemon...")

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		// If no config specified, try looking for config.yaml in current dir
		if cfgPath == "" {
			log.Println("DEBUG: No config path specified, searching for default config.yaml...")
			if _, err := os.Stat("config.yaml"); err == nil {
				cfgPath = "config.yaml"
				log.Printf("DEBUG: Using default configuration file: %s", cfgPath)
			}
		}

		mgr := tunnel.NewManager()
		srv := daemon.NewServer(mgr)
		
		log.Println("DEBUG: Starting IPC Server...")
		if err := srv.Start(); err != nil {
			log.Fatalf("CRITICAL: Failed to start IPC daemon: %v", err)
		}
		defer srv.Stop()
		log.Printf("IPC Server successfully listening on: %s", daemon.SocketPath)

		// Load initial config
		if cfgPath != "" {
			log.Printf("DEBUG: Loading configuration from %s...", cfgPath)
			cfg, err := config.LoadConfig(cfgPath)
			if err != nil {
				log.Printf("Error: Failed to load config %s: %v", cfgPath, err)
			} else {
				log.Println("DEBUG: Resolving tunnels from config...")
				resolved, err := config.ResolveTunnels(cfg)
				if err != nil {
					log.Printf("Error: Failed to resolve tunnels from %s: %v", cfgPath, err)
				} else {
					log.Printf("DEBUG: Found %d tunnels to establish.", len(resolved))
					var wg sync.WaitGroup
					for _, r := range resolved {
						wg.Add(1)
						go func(rt config.ResolvedTunnel) {
							defer wg.Done()
							log.Printf("DEBUG: Adding tunnel %s...", rt.Name)
							if err := mgr.Add(rt); err != nil {
								log.Printf("Error adding tunnel %s: %v", rt.Name, err)
							}
						}(r)
					}
					wg.Wait()
				}
			}
		} else {
			log.Println("Note: No configuration file loaded. Daemon waiting for manual 'add' commands.")
		}

		log.Println("Daemon is fully initialized and operational. Press Ctrl+C to stop.")
		s := <-sigs
		log.Printf("Signal received: %v. Shutting down (timeout %s)...", s, shutdownTimeout)

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := mgr.Shutdown(ctx); err != nil {
			log.Printf("Shutdown completed with error: %v", err)
		} else {
			log.Println("Shutdown complete; all tunnels drained cleanly.")
		}
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of active tunnels",
	Run: func(cmd *cobra.Command, args []string) {
		cli := daemon.NewClient()
		tunnels, err := cli.List()
		if err != nil {
			fmt.Printf("Error: %v (Is daemon running?)\n", err)
			os.Exit(1)
		}
		if len(tunnels) == 0 {
			fmt.Println("No active tunnels.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "LOCAL\tSERVER\tREMOTE")
		for _, rt := range tunnels {
			if rt.LocalPort == 0 && rt.Server == "" {
				// daemon predates structured status; only the name is known
				fmt.Fprintf(w, "-\t%s\t-\n", rt.Name)
				continue
			}
			remoteHost := rt.RemoteHost
			if remoteHost == "" {
				remoteHost = config.DefaultRemoteHost
			}
			remote := net.JoinHostPort(remoteHost, strconv.Itoa(rt.RemotePort))
			fmt.Fprintf(w, "%d\t%s\t%s\n", rt.LocalPort, rt.Server, remote)
		}
		w.Flush()
	},
}

var addCmd = &cobra.Command{
	Use:   "add [localPort] [serverAlias:remotePort]",
	Short: "Dynamically add a new tunnel mapping",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		localPort, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Println("Invalid local port")
			return
		}

		parts := strings.Split(args[1], ":")
		if len(parts) != 2 {
			fmt.Println("Invalid remote format, expected server:port")
			return
		}
		
		remotePort, err := strconv.Atoi(parts[1])
		if err != nil {
			fmt.Println("Invalid remote port")
			return
		}

		save, _ := cmd.Flags().GetBool("save")
		remoteHost, _ := cmd.Flags().GetString("remote-host")
		if remoteHost == "" {
			remoteHost = config.DefaultRemoteHost
		}
		server := parts[0]
		portMapping := fmt.Sprintf("%d:%d", localPort, remotePort)

		sshCfgPath, _ := cmd.Flags().GetString("ssh-config")
		if sshCfgPath == "" {
			sshCfgPath = config.DefaultSSHConfigPath()
		}
		ss, err := config.ResolveServer(sshCfgPath, server)
		if err != nil {
			fmt.Printf("Failed to resolve server %q from %s: %v\n", server, sshCfgPath, err)
			return
		}

		tunnelCfg := config.ResolvedTunnel{
			Name:         fmt.Sprintf("%s-%s", server, portMapping),
			Server:       server,
			HostName:     ss.HostName,
			Port:         ss.Port,
			User:         ss.User,
			KeyPath:      ss.KeyPath,
			ProxyCommand: ss.ProxyCommand,
			LocalPort:    localPort,
			RemoteHost:   remoteHost,
			RemotePort:   remotePort,
		}

		cli := daemon.NewClient()
		if err := cli.Add(tunnelCfg); err != nil {
			fmt.Printf("Failed to add tunnel: %v\n", err)
			return
		}
		fmt.Println("Successfully added tunnel.")

		if save {
			cfgFile, _ := cmd.Flags().GetString("config")
			if cfgFile == "" {
				cfgFile = "config.yaml" // Default search
			}
			cfg, err := config.LoadConfig(cfgFile)
			if err != nil {
				fmt.Printf("Warning: Created tunnel but failed to load config for saving: %v\n", err)
				return
			}
			config.AddTunnelToConfig(cfg, server, portMapping, remoteHost)
			if err := config.SaveConfig(cfgFile, cfg); err != nil {
				fmt.Printf("Error: Failed to save config to %s: %v\n", cfgFile, err)
			} else {
				fmt.Printf("Changes persisted to %s\n", cfgFile)
			}
		}
	},
}

// targetKind is how an rm argument addresses tunnels.
type targetKind int

const (
	targetLocalPort targetKind = iota // "1234": the tunnel bound to a local port
	targetServer                      // "gpu-server": every tunnel of a server alias
	targetName                        // "gpu-server-1234:80": legacy full name
)

// classifyTarget decides the addressing mode of an rm argument. Numeric args
// are always ports (even out-of-range ones, so the user gets a port error
// rather than a confusing server lookup failure).
func classifyTarget(arg string) targetKind {
	if _, err := strconv.Atoi(arg); err == nil {
		return targetLocalPort
	}
	if strings.Contains(arg, ":") {
		return targetName
	}
	return targetServer
}

func parseLocalPort(arg string) (int, error) {
	port, err := strconv.Atoi(arg)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid local port %q", arg)
	}
	return port, nil
}

func removeTarget(cli *daemon.Client, arg string) ([]config.ResolvedTunnel, error) {
	switch classifyTarget(arg) {
	case targetLocalPort:
		port, err := parseLocalPort(arg)
		if err != nil {
			return nil, err
		}
		return cli.RemoveByLocalPort(port)
	case targetName:
		return cli.Remove(arg)
	default:
		return cli.RemoveByServer(arg)
	}
}

// persistRemovals drops the removed tunnels from the config file using the
// authoritative metadata returned by the daemon.
func persistRemovals(cmd *cobra.Command, removed []config.ResolvedTunnel) {
	cfgFile, _ := cmd.Flags().GetString("config")
	if cfgFile == "" {
		cfgFile = "config.yaml"
	}
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		fmt.Printf("Warning: tunnels removed but failed to load config for saving: %v\n", err)
		return
	}

	changed := false
	for _, rt := range removed {
		server := rt.Server
		if server == "" {
			// tunnel predates the Server field; fall back to name parsing
			if idx := strings.LastIndex(rt.Name, "-"); idx != -1 {
				server = rt.Name[:idx]
			}
		}
		if server == "" {
			fmt.Printf("Warning: cannot determine server for %s; edit %s manually\n", rt.Name, cfgFile)
			continue
		}
		if config.RemoveTunnelTargetFromConfig(cfg, server, rt.PortMapping()) {
			changed = true
		} else {
			fmt.Printf("Note: %s not found in %s (already gone or part of a range)\n", rt.Name, cfgFile)
		}
	}
	if !changed {
		return
	}
	if err := config.SaveConfig(cfgFile, cfg); err != nil {
		fmt.Printf("Error: failed to save config to %s: %v\n", cfgFile, err)
		return
	}
	fmt.Printf("Changes persisted to %s\n", cfgFile)
}

// rmCompletions asks the running daemon for active tunnels and offers their
// local ports (primary) and server aliases (bulk removal) as candidates.
func rmCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	tunnels, err := daemon.NewClient().List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	used := make(map[string]bool, len(args))
	for _, a := range args {
		used[a] = true
	}

	var comps []string
	serverCounts := make(map[string]int)
	var serverOrder []string
	for _, rt := range tunnels {
		if rt.LocalPort > 0 {
			port := strconv.Itoa(rt.LocalPort)
			if !used[port] {
				comps = append(comps, fmt.Sprintf("%s\t%s:%d", port, rt.Server, rt.RemotePort))
			}
		} else if rt.Name != "" && !used[rt.Name] {
			comps = append(comps, rt.Name)
		}
		if rt.Server != "" {
			if serverCounts[rt.Server] == 0 {
				serverOrder = append(serverOrder, rt.Server)
			}
			serverCounts[rt.Server]++
		}
	}
	for _, server := range serverOrder {
		if !used[server] {
			comps = append(comps, fmt.Sprintf("%s\tall %d tunnel(s)", server, serverCounts[server]))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

var rmCmd = &cobra.Command{
	Use:   "rm [localPort|serverAlias|tunnelName]...",
	Short: "Dynamically stop and remove tunnel mappings",
	Long: `Remove active tunnels. Each target may be:
  - a local port (e.g. "1234"), the primary form: local ports are unique
  - a server alias (e.g. "gpu-server") to remove all of its tunnels
  - a full tunnel name (e.g. "gpu-server-1234:80"), the legacy form`,
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: rmCompletions,
	Run: func(cmd *cobra.Command, args []string) {
		save, _ := cmd.Flags().GetBool("save")
		cli := daemon.NewClient()

		var removed []config.ResolvedTunnel
		failed := false
		for _, arg := range args {
			batch, err := removeTarget(cli, arg)
			if err != nil {
				fmt.Printf("Failed to remove %q: %v\n", arg, err)
				failed = true
				continue
			}
			for _, rt := range batch {
				server := rt.Server
				if server == "" {
					server = rt.Name
				}
				fmt.Printf("Removed %s:%d (local %d)\n", server, rt.RemotePort, rt.LocalPort)
			}
			removed = append(removed, batch...)
		}

		if save && len(removed) > 0 {
			persistRemovals(cmd, removed)
		}
		if failed {
			os.Exit(1)
		}
	},
}

func main() {
	startCmd.Flags().StringP("config", "c", "", "Optional config file to load on start")
	
	addCmd.Flags().BoolP("save", "s", false, "Persist the new tunnel to the config file")
	addCmd.Flags().StringP("config", "c", "", "Path to config file for persistence")
	addCmd.Flags().String("remote-host", config.DefaultRemoteHost, "Remote host to dial from the SSH server (default: localhost)")
	addCmd.Flags().String("ssh-config", "", "Path to ssh_config for resolving the server alias (default: ~/.ssh/config)")
	
	rmCmd.Flags().BoolP("save", "s", false, "Remove the tunnel from the config file as well")
	rmCmd.Flags().StringP("config", "c", "", "Path to config file for persistence")

	rootCmd.AddCommand(startCmd, statusCmd, addCmd, rmCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
