package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/daemon"
	"gosh-tunnel/internal/tunnel"
)

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
		log.Printf("DEBUG: Signal received: %v. Shutting down daemon...", s)
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of active tunnels",
	Run: func(cmd *cobra.Command, args []string) {
		cli := daemon.NewClient()
		tunnels, err := cli.Status()
		if err != nil {
			fmt.Printf("Error: %v (Is daemon running?)\n", err)
			return
		}
		if len(tunnels) == 0 {
			fmt.Println("No active tunnels.")
			return
		}
		fmt.Println("Active Tunnels:")
		for _, t := range tunnels {
			fmt.Printf(" - %s\n", t)
		}
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

		// Dummy resolving for hot-add MVP - normally we would parse ssh_config here too
		tunnelCfg := config.ResolvedTunnel{
			Name:       fmt.Sprintf("%s-%d:%d", parts[0], localPort, remotePort),

			HostName:   parts[0], // Assuming HostName = Alias for hot adds unless resolved
			Port:       "22", // Default
			User:       os.Getenv("USER"),
			KeyPath:    "", // Let engine handle defaults/agent
			LocalPort:  localPort,
			RemotePort: remotePort,
		}

		cli := daemon.NewClient()
		if err := cli.Add(tunnelCfg); err != nil {
			fmt.Printf("Failed to add tunnel: %v\n", err)
		} else {
			fmt.Println("Successfully added tunnel.")
		}
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm [tunnelName]",
	Short: "Dynamically stop and remove a tunnel mapping",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cli := daemon.NewClient()
		if err := cli.Remove(args[0]); err != nil {
			fmt.Printf("Failed to remove tunnel: %v\n", err)
		} else {
			fmt.Println("Successfully removed tunnel.")
		}
	},
}

func main() {
	startCmd.Flags().StringP("config", "c", "", "Optional config file to load on start")
	rootCmd.AddCommand(startCmd, statusCmd, addCmd, rmCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
