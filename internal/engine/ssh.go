package engine

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Engine struct {
	HostName     string
	Port         string
	User         string
	KeyPath      string
	ProxyCommand string

	mu     sync.Mutex
	Client *ssh.Client
}

func parseKey(path string) (ssh.AuthMethod, error) {
	if path == "" {
		return nil, fmt.Errorf("empty key path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, err
	}
	return ssh.PublicKeys(signer), nil
}

func agentAuth() ssh.AuthMethod {
	sock, ok := os.LookupEnv("SSH_AUTH_SOCK")
	if !ok {
		return nil
	}
	log.Printf("DEBUG: Found SSH_AUTH_SOCK: %s, attempting to connect...", sock)
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		log.Printf("Warning: SSH_AUTH_SOCK set but failed to connect (timeout 2s): %v", err)
		return nil
	}
	log.Printf("DEBUG: Successfully connected to SSH Agent.")
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers)
}

func New(host, port, user, keyPath string) *Engine {
	return &Engine{
		HostName: host,
		Port:     port,
		User:     user,
		KeyPath:  keyPath,
	}
}

// NewWithProxy is like New but allows specifying a ProxyCommand (already token-expanded).
func NewWithProxy(host, port, user, keyPath, proxyCommand string) *Engine {
	return &Engine{
		HostName:     host,
		Port:         port,
		User:         user,
		KeyPath:      keyPath,
		ProxyCommand: proxyCommand,
	}
}

// Connect dial the SSH server and starts a keep-alive routine.
func (e *Engine) Connect() error {
	e.mu.Lock()
	if e.Client != nil {
		e.mu.Unlock()
		return nil // Already connected
	}
	user := e.User
	host := e.HostName
	port := e.Port
	keyPath := e.KeyPath
	proxyCommand := e.ProxyCommand
	e.mu.Unlock()

	var auths []ssh.AuthMethod
	var tried []string

	if keyPath != "" {
		tried = append(tried, fmt.Sprintf("file:%s", keyPath))
		if auth, err := parseKey(keyPath); err == nil {
			auths = append(auths, auth)
		} else {
			log.Printf("Warning: failed to use key %s: %v", keyPath, err)
		}
	}

	if len(auths) == 0 {
		home, _ := os.UserHomeDir()
		defaults := []string{
			filepath.Join(home, ".ssh", "id_ed25519"),
			filepath.Join(home, ".ssh", "id_rsa"),
		}
		for _, d := range defaults {
			tried = append(tried, fmt.Sprintf("default-file:%s", d))
			if auth, err := parseKey(d); err == nil {
				auths = append(auths, auth)
				break
			}
		}
	}

	if a := agentAuth(); a != nil {
		tried = append(tried, "ssh-agent")
		auths = append(auths, a)
	}

	if len(auths) == 0 {
		return fmt.Errorf("no valid SSH authentication methods found (tried: %v)", tried)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, port)

	var (
		client *ssh.Client
		err    error
	)
	if proxyCommand != "" {
		log.Printf("DEBUG: Using ProxyCommand for %s: %s", addr, proxyCommand)
		client, err = dialViaProxyCommand(proxyCommand, addr, cfg)
	} else {
		client, err = ssh.Dial("tcp", addr, cfg)
	}
	if err != nil {
		return fmt.Errorf("ssh dial failed: %w", err)
	}

	e.mu.Lock()
	if e.Client != nil {
		client.Close()
		e.mu.Unlock()
		return nil
	}
	e.Client = client
	e.mu.Unlock()

	go e.keepAlive(client)
	return nil
}

// dialViaProxyCommand spawns the proxy command, wires its stdio into a net.Conn,
// and performs the SSH handshake over it.
func dialViaProxyCommand(proxyCommand, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	cmd := exec.Command("sh", "-c", proxyCommand)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proxy command start: %w", err)
	}

	conn := newStdioConn(stdout, stdin, cmd)

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake over ProxyCommand: %w", err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// stdioConn adapts a ProxyCommand's stdio into a net.Conn.
type stdioConn struct {
	r      io.ReadCloser
	w      io.WriteCloser
	cmd    *exec.Cmd
	once   sync.Once
	closed chan struct{}
}

func newStdioConn(r io.ReadCloser, w io.WriteCloser, cmd *exec.Cmd) *stdioConn {
	return &stdioConn{r: r, w: w, cmd: cmd, closed: make(chan struct{})}
}

func (s *stdioConn) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *stdioConn) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *stdioConn) Close() error {
	var firstErr error
	s.once.Do(func() {
		_ = s.w.Close()
		_ = s.r.Close()
		if s.cmd != nil && s.cmd.Process != nil {
			done := make(chan error, 1)
			go func() { done <- s.cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = s.cmd.Process.Kill()
				<-done
			}
		}
		close(s.closed)
	})
	if firstErr != nil && !errors.Is(firstErr, os.ErrClosed) {
		return firstErr
	}
	return nil
}

type stdioAddr struct{ label string }

func (a stdioAddr) Network() string { return "proxycommand" }
func (a stdioAddr) String() string  { return a.label }

func (s *stdioConn) LocalAddr() net.Addr               { return stdioAddr{"local"} }
func (s *stdioConn) RemoteAddr() net.Addr              { return stdioAddr{"remote"} }
func (s *stdioConn) SetDeadline(_ time.Time) error     { return nil }
func (s *stdioConn) SetReadDeadline(_ time.Time) error { return nil }
func (s *stdioConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

// GetClient returns current ssh client, reconnecting if disconnected.
func (e *Engine) GetClient() (*ssh.Client, error) {
	e.mu.Lock()
	client := e.Client
	e.mu.Unlock()

	if client == nil {
		if err := e.Connect(); err != nil {
			return nil, err
		}
	} else {
		_, _, err := client.SendRequest("keepalive@gosh.tunnel", true, nil)
		if err != nil {
			log.Printf("GetClient detected dead connection for %s, reconnecting...", e.HostName)
			client.Close()

			e.mu.Lock()
			if e.Client == client {
				e.Client = nil
			}
			e.mu.Unlock()

			if err := e.Connect(); err != nil {
				return nil, err
			}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Client, nil
}

func (e *Engine) keepAlive(client *ssh.Client) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		_, _, err := client.SendRequest("keepalive@gosh.tunnel", true, nil)
		if err != nil {
			log.Printf("Keepalive failed for %s. Closing connection.", e.HostName)
			client.Close()
			e.mu.Lock()
			if e.Client == client {
				e.Client = nil
			}
			e.mu.Unlock()
			return
		}
	}
}
