package engine

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
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
	defer e.mu.Unlock()

	if e.Client != nil {
		return nil // Already connected
	}

	auth, err := parseKey(e.KeyPath)
	if err != nil {
		return fmt.Errorf("failed to parse SSH key (%s): %w", e.KeyPath, err)
	}

	config := &ssh.ClientConfig{
		User: e.User,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(e.HostName, e.Port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial failed: %w", err)
	}

	e.Client = client
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
