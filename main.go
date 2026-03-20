package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

		mgr := tunnel.NewManager()
		srv := daemon.NewServer(mgr)
		
		if err := srv.Start(); err != nil {
			log.Fatalf("Failed to start daemon: %v", err)
		}
		defer srv.Stop()

		// Optional: Load initial config
		if cfgPath != "" {
			if cfg, err := config.LoadConfig(cfgPath); err == nil {
				if resolved, err := config.ResolveTunnels(cfg); err == nil {
					for _, r := range resolved {
						_ = mgr.Add(r)
					}
				}
			}
		}

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Println("Shutting down daemon...")
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
			KeyPath:    os.Getenv("HOME") + "/.ssh/id_rsa",
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
