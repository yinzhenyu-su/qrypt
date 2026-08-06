// Package httpclient provides the shared building blocks for driver HTTP
// requests: JSON request construction, response body reading, and JSON
// decoding. Drivers keep their own orchestration (auth header injection,
// retry loops, metric reporting, error classification) but no longer
// hand-roll request/response plumbing.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// NewJSONRequest builds an HTTP request whose body is the JSON encoding of
// body, with Content-Type set to application/json. Pass nil to send no body.
func NewJSONRequest(ctx context.Context, method, rawURL string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode json body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// ReadBody reads and closes the response body. The caller owns the returned
// bytes; use it instead of the io.ReadAll + resp.Body.Close() pair so a
// single place owns read/close semantics.
func ReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// DecodeJSON unmarshals JSON bytes into out, wrapping the error with the
// byte length for context.
func DecodeJSON(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode json (%d bytes): %w", len(body), err)
	}
	return nil
}
