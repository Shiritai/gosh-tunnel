package engine

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Engine struct {
	HostName string
	Port     string
	User     string
	KeyPath  string

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

// Connect dial the SSH server and starts a keep-alive routine.
func (e *Engine) Connect() error {
	e.mu.Lock()
	if e.Client != nil {
		e.mu.Unlock()
		return nil // Already connected
	}
	// We use the lock only to gather configuration, then release it for the Dial
	user := e.User
	host := e.HostName
	port := e.Port
	keyPath := e.KeyPath
	e.mu.Unlock()

	var auths []ssh.AuthMethod
	var tried []string

	// 1. Try specified key path
	if keyPath != "" {
		tried = append(tried, fmt.Sprintf("file:%s", keyPath))
		if auth, err := parseKey(keyPath); err == nil {
			auths = append(auths, auth)
		} else {
			log.Printf("Warning: failed to use key %s: %v", keyPath, err)
		}
	}

	// 2. Try default keys
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

	// 3. Always include SSH Agent
	if agent := agentAuth(); agent != nil {
		tried = append(tried, "ssh-agent")
		auths = append(auths, agent)
	}

	if len(auths) == 0 {
		return fmt.Errorf("no valid SSH authentication methods found (tried: %v)", tried)
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial failed: %w", err)
	}

	e.mu.Lock()
	if e.Client != nil {
		// Someone else connected while we were dialing
		client.Close()
		e.mu.Unlock()
		return nil
	}
	e.Client = client
	e.mu.Unlock()

	go e.keepAlive(client)
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
		// Quick check if connection is still alive
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
