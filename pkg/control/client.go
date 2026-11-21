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

// Delete sends a DELETE request to a mutable control endpoint.
func (c *Client) Delete(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	return c.do(req, path)
}

// PostJSON sends a JSON body to a mutable control endpoint.
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
	if id := operationID(ctx); id != "" {
		req.Header.Set(OperationIDHeader, id)
	}
	return c.do(req, path)
}

// OpenStream issues a GET and returns the response body for the caller to
// consume and close. It is used for streaming data (file reads) so the client
// never buffers the whole payload.
func (c *Client) OpenStream(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, newTransportError(path, resp.StatusCode, body)
	}
	return resp.Body, nil
}

// TransportError is a non-2xx control response. Code, Retryable, and Details
// come from the error envelope; Body always holds the human-readable message so
// callers that match on text keep working, and Raw holds the untouched body for
// diagnostics.
type TransportError struct {
	Path      string
	Status    int
	Body      string
	Code      ErrorCode
	Retryable bool
	Details   map[string]any
	Raw       string
}

func (e *TransportError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("control: %s returned status %d (%s): %s", e.Path, e.Status, e.Code, e.Body)
	}
	return fmt.Sprintf("control: %s returned status %d: %s", e.Path, e.Status, e.Body)
}

// newTransportError decodes an error envelope when the server sent one and
// falls back to the raw text otherwise, so every non-2xx answer reaches the
// caller as a TransportError with a usable message.
func newTransportError(path string, status int, body []byte) *TransportError {
	raw := strings.TrimSpace(string(body))
	transport := &TransportError{Path: path, Status: status, Body: raw, Raw: raw}
	var envelope ErrorEnvelope
	if raw != "" && json.Unmarshal(body, &envelope) == nil && envelope.Error.Code != "" {
		transport.Code = envelope.Error.Code
		transport.Retryable = envelope.Error.Retryable
		transport.Details = envelope.Error.Details
		if envelope.Error.Message != "" {
			transport.Body = envelope.Error.Message
		}
	}
	return transport
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
		return nil, newTransportError(path, resp.StatusCode, body)
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
