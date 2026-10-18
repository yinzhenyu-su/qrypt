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
	parsed, err := parseEndpoint(socketPath)
	if err != nil {
		return nil, err
	}
	if baseURL, ok := clientHTTPBaseURL(socketPath, parsed); ok {
		return &Client{
			socketPath: baseURL,
			baseURL:    baseURL,
			httpClient: &http.Client{},
		}, nil
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialEndpoint(ctx, socketPath)
		},
	}
	clientEndpoint := parsed.address
	if parsed.network == "pipe" {
		clientEndpoint = "pipe:" + parsed.address
	}
	return &Client{
		socketPath: clientEndpoint,
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

// TransportError is a non-2xx control response. It preserves the response
// status and body so a caller (the CLI) can classify the server's failure,
// while Error() keeps the historical message shape.
type TransportError struct {
	Path   string
	Status int
	Body   string
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("control: %s returned status %d: %s", e.Path, e.Status, e.Body)
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
		return nil, &TransportError{Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
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

func clientHTTPBaseURL(endpoint string, parsed endpointInfo) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	if parsed.network != "tcp" {
		return "", false
	}
	switch {
	case strings.HasPrefix(endpoint, "http://"):
		return endpoint, true
	case strings.HasPrefix(endpoint, "tcp:"):
		return "http://" + parsed.address, true
	}
	return "http://" + parsed.address, true
}
