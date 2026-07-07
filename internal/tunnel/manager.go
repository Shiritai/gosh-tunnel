package tunnel

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"gosh-tunnel/internal/config"
	"gosh-tunnel/internal/engine"
)

// safeGo runs fn in a goroutine and recovers from panics so a single bad
// connection or listener cannot crash the daemon.
func safeGo(label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in %s: %v\n%s", label, r, debug.Stack())
			}
		}()
		fn()
	}()
}

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
	conns   sync.WaitGroup // tracks in-flight handleConnection goroutines
}

func NewManager() *Manager {
	return &Manager{
		tunnels: make(map[string]*Tunnel),
		engines: make(map[string]*engine.Engine),
	}
}

// Shutdown stops accepting new connections on every tunnel, cancels the
// per-tunnel context, closes every cached SSH engine, and waits for in-flight
// connections to drain. If ctx expires first it returns ctx.Err() but still
// performs the cancel/close steps.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	tunnels := make([]*Tunnel, 0, len(m.tunnels))
	for name, t := range m.tunnels {
		tunnels = append(tunnels, t)
		delete(m.tunnels, name)
	}
	engines := m.engines
	m.engines = make(map[string]*engine.Engine)
	m.mu.Unlock()

	for _, t := range tunnels {
		t.cancel()
		if t.Listener != nil {
			t.Listener.Close()
		}
	}
	for _, eng := range engines {
		eng.Close()
	}

	done := make(chan struct{})
	go func() {
		m.conns.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		log.Printf("Shutdown: timed out waiting for %d tunnel(s) to drain in-flight connections", len(tunnels))
		return ctx.Err()
	}
}

// Add starts a new tunnel or returns an error if one already exists
func (m *Manager) Add(c config.ResolvedTunnel) error {
	m.mu.Lock()
	if _, exists := m.tunnels[c.Name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("tunnel %s already exists", c.Name)
	}

	engineKey := c.User + "@" + c.HostName + ":" + c.Port + "|" + c.ProxyCommand
	eng, ok := m.engines[engineKey]
	if !ok {
		eng = engine.NewWithProxy(c.HostName, c.Port, c.User, c.KeyPath, c.ProxyCommand)
		m.engines[engineKey] = eng
	}
	m.mu.Unlock()
	
	// Connect to engine outside of manager lock
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

	m.mu.Lock()
	if _, exists := m.tunnels[c.Name]; exists {
		m.mu.Unlock()
		cancel()
		t.Listener.Close() // Cleanup
		return fmt.Errorf("tunnel %s was concurrently created", c.Name)
	}
	m.tunnels[c.Name] = t
	m.mu.Unlock()
	return nil
}

// removeLocked stops t and deletes it from the map. Caller must hold m.mu.
func (m *Manager) removeLocked(name string, t *Tunnel) {
	log.Printf("Stopping tunnel: %s", name)
	t.cancel()
	if t.Listener != nil {
		t.Listener.Close()
	}
	delete(m.tunnels, name)
}

// Remove stops and removes a specific tunnel by its full name and returns the
// removed tunnel's config so callers can persist the change authoritatively.
func (m *Manager) Remove(name string) (config.ResolvedTunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, exists := m.tunnels[name]
	if !exists {
		return config.ResolvedTunnel{}, fmt.Errorf("tunnel %s not found", name)
	}
	m.removeLocked(name, t)
	return t.Config, nil
}

// RemoveByLocalPort stops and removes the tunnel bound to the given local
// port. Local ports are unique per host, so this identifies exactly one tunnel.
func (m *Manager) RemoveByLocalPort(port int) (config.ResolvedTunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, t := range m.tunnels {
		if t.Config.LocalPort == port {
			m.removeLocked(name, t)
			return t.Config, nil
		}
	}
	return config.ResolvedTunnel{}, fmt.Errorf("no tunnel on local port %d", port)
}

// RemoveByServer stops and removes every tunnel belonging to the given server
// alias and returns their configs. It fails if the alias has no tunnels.
func (m *Manager) RemoveByServer(server string) ([]config.ResolvedTunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var removed []config.ResolvedTunnel
	for name, t := range m.tunnels {
		if t.Config.Server == server {
			m.removeLocked(name, t)
			removed = append(removed, t.Config)
		}
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("no tunnels for server %s", server)
	}
	return removed, nil
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

// List returns the configs of active tunnels sorted by server then local port.
func (m *Manager) List() []config.ResolvedTunnel {
	m.mu.Lock()
	defer m.mu.Unlock()

	list := make([]config.ResolvedTunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		list = append(list, t.Config)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Server != list[j].Server {
			return list[i].Server < list[j].Server
		}
		return list[i].LocalPort < list[j].LocalPort
	})
	return list
}

func (m *Manager) startTunnel(ctx context.Context, t *Tunnel) error {
	localAddr := fmt.Sprintf("127.0.0.1:%d", t.Config.LocalPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w", localAddr, err)
	}
	t.Listener = listener

	safeGo("tunnel.accept["+t.Config.Name+"]", func() {
		safeGo("tunnel.closer["+t.Config.Name+"]", func() {
			<-ctx.Done()
			listener.Close()
		})

		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					log.Printf("Accept error on %s: %v", localAddr, err)
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			m.conns.Add(1)
			safeGo("tunnel.handle["+t.Config.Name+"]", func() {
				defer m.conns.Done()
				m.handleConnection(ctx, t, conn)
			})
		}
	})
	return nil
}

func (m *Manager) handleConnection(ctx context.Context, t *Tunnel, localConn net.Conn) {
	defer localConn.Close()

	sshClient, err := t.Engine.GetClient()
	if err != nil {
		log.Printf("[%s] SSH Client disconnected: %v", t.Config.Name, err)
		return
	}

	remoteHost := t.Config.RemoteHost
	if remoteHost == "" {
		remoteHost = "localhost"
	}
	remoteAddr := net.JoinHostPort(remoteHost, fmt.Sprintf("%d", t.Config.RemotePort))
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
