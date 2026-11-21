package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/httpapi"
	"github.com/yinzhenyu/qrypt/pkg/control"
)

func TestHTTPCommandHelp(t *testing.T) {
	out, err := executeRoot(t, "http")
	if err != nil {
		t.Fatalf("http help: %v\n%s", err, out)
	}
	if !strings.Contains(out, "serve") || !strings.Contains(out, "Serve the HTTP API") {
		t.Fatalf("unexpected http help output:\n%s", out)
	}
}

func TestHTTPListenValidation(t *testing.T) {
	base := t.TempDir()
	configPath, _ := writeCoreServeTestConfig(t, base)
	out, err := executeRoot(t, "http", "serve", "--config", configPath, "--listen", "not-an-address")
	if err == nil {
		t.Fatalf("expected invalid listen error, got output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "invalid --listen address") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPNonLoopbackRequiresTLS(t *testing.T) {
	base := t.TempDir()
	configPath, _ := writeCoreServeTestConfig(t, base)
	out, err := executeRoot(t, "http", "serve", "--config", configPath, "--listen", "0.0.0.0:18080")
	if err == nil {
		t.Fatalf("expected TLS requirement error, got output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "without TLS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPWriteVisibleToFS(t *testing.T) {
	// t.TempDir() lives under /var/folders and the derived control socket path
	// can exceed the Unix sun_path limit; keep the base short.
	base, err := os.MkdirTemp("", "qrypt-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)
	serveDone := make(chan error, 1)
	go func() {
		_, err := executeRoot(t, "core", "serve", "--foreground", "--keep", "--config", configPath)
		serveDone <- err
	}()
	defer func() {
		_, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath)
		<-serveDone
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _ := executeRoot(t, "core", "status", "--json", "--config", configPath)
		if strings.Contains(out, `"state": "running"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident Core did not start:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}

	out, err := executeRoot(t, "core", "endpoint", "--json", "--config", configPath)
	if err != nil {
		t.Fatalf("core endpoint: %v\n%s", err, out)
	}
	var endpoint struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(out), &endpoint); err != nil {
		t.Fatalf("decode endpoint: %v\n%s", err, out)
	}
	client, err := control.NewClient(endpoint.Endpoint)
	if err != nil {
		t.Fatalf("control client: %v", err)
	}
	gateway := httptest.NewServer(httpapi.New(httpapi.NewClientCore(client), httpapi.Options{Token: "test-token"}).Handler())
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/fs/upload?path=/local/http.txt", strings.NewReader("over http"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http upload status = %d", resp.StatusCode)
	}

	out, err = executeRoot(t, "fs", "list", "--json", "/local", "--config", configPath)
	if err != nil {
		t.Fatalf("fs list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "http.txt") {
		t.Fatalf("http upload not visible through fs list:\n%s", out)
	}
	out, err = executeRoot(t, "fs", "cat", "/local/http.txt", "--config", configPath)
	if err != nil {
		t.Fatalf("fs cat: %v\n%s", err, out)
	}
	if out != "over http" {
		t.Fatalf("fs cat = %q, want %q", out, "over http")
	}
}
