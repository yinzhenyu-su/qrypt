package cli

import (
	"strings"
	"testing"
)

func TestHTTPCommandHelp(t *testing.T) {
	out, err := executeRoot(t, "http")
	if err != nil {
		t.Fatalf("http help: %v\n%s", err, out)
	}
	if !strings.Contains(out, "serve") || !strings.Contains(out, "read-only HTTP API") {
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
