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
	Name    string                `json:"name,omitempty"`
}

type Response struct {
	Success bool     `json:"success"`
	Message string   `json:"message,omitempty"`
	Tunnels []string `json:"tunnels,omitempty"`
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
			go s.handleConnection(conn)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.listener != nil {
		s.listener.Close()
	}
	os.Remove(SocketPath)
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

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
		if err := s.manager.Remove(req.Name); err != nil {
			s.sendRes(conn, Response{Success: false, Message: err.Error()})
		} else {
			s.sendRes(conn, Response{Success: true, Message: fmt.Sprintf("Removed tunnel %s", req.Name)})
		}
	case "status":
		tunnels := s.manager.Status()
		s.sendRes(conn, Response{Success: true, Tunnels: tunnels})
	default:
		s.sendRes(conn, Response{Success: false, Message: "unknown command"})
	}
}

func (s *Server) sendRes(conn net.Conn, res Response) {
	_ = json.NewEncoder(conn).Encode(res)
}
