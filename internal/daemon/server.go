package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/tunnel"
)

type Request struct {
	Command string                `json:"command"`
	Tunnel  config.ResolvedTunnel `json:"tunnel,omitempty"`
	// rm addressing modes, checked in order: LocalPort, Server, Name.
	Name      string `json:"name,omitempty"`
	LocalPort int    `json:"local_port,omitempty"`
	Server    string `json:"server,omitempty"`
}

type Response struct {
	Success bool     `json:"success"`
	Message string   `json:"message,omitempty"`
	Tunnels []string `json:"tunnels,omitempty"`
	// Details is the structured counterpart of Tunnels.
	Details []config.ResolvedTunnel `json:"details,omitempty"`
	// Removed reports what an rm actually tore down so clients can persist
	// config changes from authoritative data instead of parsing names.
	Removed []config.ResolvedTunnel `json:"removed,omitempty"`
}

var SocketPath = "/tmp/gosh-tunnel.sock"

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		SocketPath = filepath.Join(home, ".gosh-tunnel.sock")
	}
}

type Server struct {
	manager  *tunnel.Manager
	listener net.Listener
}

func NewServer(mgr *tunnel.Manager) *Server {
	return &Server{manager: mgr}
}

func (s *Server) Start() error {
	log.Printf("DEBUG: Cleaning up and listening on Unix socket: %s", SocketPath)
	_ = os.Remove(SocketPath)
	l, err := net.Listen("unix", SocketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket %s: %w", SocketPath, err)
	}
	s.listener = l

	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				if strings.Contains(err.Error(), "use of closed network connection") {
					return
				}
				log.Printf("IPC Server accept error: %v", err)
				continue
			}
			log.Printf("DEBUG: IPC Server accepted connection from %s", conn.RemoteAddr())
			go s.handleConnection(conn)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.listener != nil {
		log.Printf("DEBUG: Stopping IPC Server and removing socket: %s", SocketPath)
		s.listener.Close()
	}
	os.Remove(SocketPath)
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	log.Printf("DEBUG: Handling IPC request from %s", conn.RemoteAddr())

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		s.sendRes(conn, Response{Success: false, Message: "invalid JSON"})
		return
	}

	switch req.Command {
	case "add":
		if err := s.manager.Add(req.Tunnel); err != nil {
			s.sendRes(conn, Response{Success: false, Message: err.Error()})
		} else {
			s.sendRes(conn, Response{Success: true, Message: fmt.Sprintf("Added tunnel %s", req.Tunnel.Name)})
		}
	case "rm":
		removed, err := s.removeTunnels(req)
		if err != nil {
			s.sendRes(conn, Response{Success: false, Message: err.Error()})
		} else {
			s.sendRes(conn, Response{Success: true, Message: fmt.Sprintf("Removed %d tunnel(s)", len(removed)), Removed: removed})
		}
	case "status":
		s.sendRes(conn, Response{Success: true, Tunnels: s.manager.Status(), Details: s.manager.List()})
	default:
		s.sendRes(conn, Response{Success: false, Message: "unknown command"})
	}
}

// removeTunnels dispatches an rm request to the manager based on which
// addressing mode the request carries.
func (s *Server) removeTunnels(req Request) ([]config.ResolvedTunnel, error) {
	switch {
	case req.LocalPort > 0:
		rt, err := s.manager.RemoveByLocalPort(req.LocalPort)
		if err != nil {
			return nil, err
		}
		return []config.ResolvedTunnel{rt}, nil
	case req.Server != "":
		return s.manager.RemoveByServer(req.Server)
	case req.Name != "":
		rt, err := s.manager.Remove(req.Name)
		if err != nil {
			return nil, err
		}
		return []config.ResolvedTunnel{rt}, nil
	default:
		return nil, fmt.Errorf("rm request must specify local_port, server, or name")
	}
}

func (s *Server) sendRes(conn net.Conn, res Response) {
	_ = json.NewEncoder(conn).Encode(res)
}
