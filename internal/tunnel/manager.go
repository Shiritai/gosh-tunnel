package tunnel

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/engine"
)

type Tunnel struct {
	Config   config.ResolvedTunnel
	Engine   *engine.Engine
	Listener net.Listener
	cancel   context.CancelFunc
}

type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*Tunnel
	engines map[string]*engine.Engine
}

func NewManager() *Manager {
	return &Manager{
		tunnels: make(map[string]*Tunnel),
		engines: make(map[string]*engine.Engine),
	}
}

// Add starts a new tunnel or returns an error if one already exists
func (m *Manager) Add(c config.ResolvedTunnel) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tunnels[c.Name]; exists {
		return fmt.Errorf("tunnel %s already exists", c.Name)
	}

	engineKey := c.User + "@" + c.HostName + ":" + c.Port
	eng, ok := m.engines[engineKey]
	if !ok {
		eng = engine.New(c.HostName, c.Port, c.User, c.KeyPath)
		m.engines[engineKey] = eng
	}
	
	if _, err := eng.GetClient(); err != nil {
		log.Printf("Warning: failed to connect engine [%s] during add: %v. Will retry on connection.", engineKey, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{
		Config: c,
		Engine: eng,
		cancel: cancel,
	}

	if err := m.startTunnel(ctx, t); err != nil {
		cancel()
		return fmt.Errorf("failed to start tunnel %s: %w", c.Name, err)
	}
	log.Printf("Started tunnel: %s", c.Name)
	m.tunnels[c.Name] = t
	return nil
}

// Remove stops and removes a specific tunnel by name
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, exists := m.tunnels[name]
	if !exists {
		return fmt.Errorf("tunnel %s not found", name)
	}

	log.Printf("Stopping tunnel: %s", name)
	t.cancel()
	if t.Listener != nil {
		t.Listener.Close()
	}
	delete(m.tunnels, name)
	return nil
}

// Status returns a list of active tunnel names
func (m *Manager) Status() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	var active []string
	for name := range m.tunnels {
		active = append(active, name)
	}
	return active
}

func (m *Manager) startTunnel(ctx context.Context, t *Tunnel) error {
	localAddr := fmt.Sprintf("127.0.0.1:%d", t.Config.LocalPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w", localAddr, err)
	}
	t.Listener = listener

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
			conn, err := listener.Accept()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				continue
			}
			go m.handleConnection(ctx, t, conn)
		}
	}()
	return nil
}

func (m *Manager) handleConnection(ctx context.Context, t *Tunnel, localConn net.Conn) {
	defer localConn.Close()

	sshClient, err := t.Engine.GetClient()
	if err != nil {
		log.Printf("[%s] SSH Client disconnected: %v", t.Config.Name, err)
		return
	}

	remoteAddr := fmt.Sprintf("127.0.0.1:%d", t.Config.RemotePort)
	remoteConn, err := sshClient.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("[%s] Failed to dial remote %s via SSH: %v", t.Config.Name, remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(remoteConn, localConn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(localConn, remoteConn)
		errc <- err
	}()

	select {
	case <-errc:
	case <-ctx.Done():
	}
}
