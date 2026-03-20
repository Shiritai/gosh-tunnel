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

func (c *Client) Remove(name string) error {
	res, err := c.sendRequest(Request{Command: "rm", Name: name})
	if err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("%s", res.Message)
	}
	return nil
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
