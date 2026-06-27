package control

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

type Client struct {
	socketPath string
	httpClient *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("control: socket path required")
	}
	socketPath = expandHome(socketPath)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: transport,
		},
	}, nil
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://qrypt"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("control: %s returned status %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) SocketPath() string {
	return c.socketPath
}
