package daemon

import (
	"encoding/json"
	"fmt"
	"net"

	"gosh-tunnel/internal/config"
)

type Client struct {
	socketPath string
}

func NewClient() *Client {
	return &Client{socketPath: SocketPath}
}

func (c *Client) sendRequest(req Request) (*Response, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("could not connect to daemon at %s: %w", c.socketPath, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}

	var res Response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) Add(tunnel config.ResolvedTunnel) error {
	res, err := c.sendRequest(Request{Command: "add", Tunnel: tunnel})
	if err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
}

func (c *Client) remove(req Request) ([]config.ResolvedTunnel, error) {
	res, err := c.sendRequest(req)
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("%s", res.Message)
	}
	return res.Removed, nil
}

// Remove tears down a tunnel by its full name (legacy addressing) and returns
// the removed tunnel's metadata.
func (c *Client) Remove(name string) ([]config.ResolvedTunnel, error) {
	return c.remove(Request{Command: "rm", Name: name})
}

// RemoveByLocalPort tears down the tunnel bound to the given local port.
func (c *Client) RemoveByLocalPort(port int) ([]config.ResolvedTunnel, error) {
	return c.remove(Request{Command: "rm", LocalPort: port})
}

// RemoveByServer tears down every tunnel belonging to a server alias.
func (c *Client) RemoveByServer(server string) ([]config.ResolvedTunnel, error) {
	return c.remove(Request{Command: "rm", Server: server})
}

func (c *Client) Status() ([]string, error) {
	res, err := c.sendRequest(Request{Command: "status"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("%s", res.Message)
	}
	return res.Tunnels, nil
}

// List returns structured metadata for active tunnels. When talking to a
// daemon that predates structured status it degrades to name-only entries.
func (c *Client) List() ([]config.ResolvedTunnel, error) {
	res, err := c.sendRequest(Request{Command: "status"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("%s", res.Message)
	}
	if res.Details == nil && len(res.Tunnels) > 0 {
		details := make([]config.ResolvedTunnel, 0, len(res.Tunnels))
		for _, name := range res.Tunnels {
			details = append(details, config.ResolvedTunnel{Name: name})
		}
		return details, nil
	}
	return res.Details, nil
}
