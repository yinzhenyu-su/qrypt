package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

type Client struct {
	socketPath string
	baseURL    string
	httpClient *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("control: socket path required")
	}
	if baseURL, ok := clientHTTPBaseURL(socketPath); ok {
		return &Client{
			baseURL:    baseURL,
			httpClient: &http.Client{},
		}, nil
	}
	socketPath = osutil.ExpandHome(socketPath)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	return c.do(req, path)
}

func (c *Client) PostJSON(ctx context.Context, path string, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, path)
}

func (c *Client) do(req *http.Request, path string) ([]byte, error) {
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

func (c *Client) url(path string) string {
	if c.baseURL != "" {
		return strings.TrimRight(c.baseURL, "/") + path
	}
	return "http://qrypt" + path
}

func clientHTTPBaseURL(endpoint string) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		return endpoint, true
	case strings.HasPrefix(endpoint, "tcp:"):
		return "http://" + strings.TrimPrefix(endpoint, "tcp:"), true
	}
	if _, _, err := net.SplitHostPort(endpoint); err == nil {
		return "http://" + endpoint, true
	}
	return "", false
}
