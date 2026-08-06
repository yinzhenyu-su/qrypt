package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/util/httpclient"
)

func TestNewJSONRequestEncodesBodyAndSetsContentType(t *testing.T) {
	req, err := httpclient.NewJSONRequest(context.Background(), http.MethodPost, "https://example.com/api", map[string]any{"name": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"name":"a"`) {
		t.Errorf("body not JSON-encoded: %s", body)
	}
}

func TestNewJSONRequestNilBodyOmitsContentType(t *testing.T) {
	req, err := httpclient.NewJSONRequest(context.Background(), http.MethodGet, "https://example.com/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want empty for nil body", ct)
	}
	if req.Body != nil && req.Body != http.NoBody {
		t.Errorf("body should be empty, got %v", req.Body)
	}
}

func TestNewJSONRequestRejectsUnencodableBody(t *testing.T) {
	_, err := httpclient.NewJSONRequest(context.Background(), http.MethodPost, "https://example.com/api", func() {})
	if err == nil {
		t.Fatal("expected error for unencodable body")
	}
}

func TestReadBodyReadsAndCloses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := httpclient.ReadBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
	if _, err := resp.Body.Read(make([]byte, 1)); err == nil {
		t.Error("response body should be closed after ReadBody")
	}
}

func TestDecodeJSON(t *testing.T) {
	var out struct {
		Name string `json:"name"`
	}
	if err := httpclient.DecodeJSON([]byte(`{"name":"x"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "x" {
		t.Errorf("Name = %q, want x", out.Name)
	}

	var bad struct{}
	if err := httpclient.DecodeJSON([]byte(`{not json`), &bad); err == nil {
		t.Error("expected error for invalid JSON")
	}
}
