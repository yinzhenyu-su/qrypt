package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFSCommandsThroughCore exercises the data plane: put/list/stat/mv run
// through the resident Core, and the FUSE-visible remote state reflects them.
func TestFSCommandsThroughCore(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-fs-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)
	remote := filepath.Join(base, "remote")
	t.Setenv("QRYPT_CORE_FS_REQUIRED", "1")

	serveRoot := NewRootCommand()
	var serveOut bytes.Buffer
	serveRoot.SetOut(&serveOut)
	serveRoot.SetErr(&serveOut)
	serveRoot.SetArgs([]string{"core", "serve", "--foreground", "--keep", "--config", configPath})
	done := make(chan error, 1)
	go func() { done <- serveRoot.Execute() }()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		_, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath)
		<-done
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := executeRoot(t, "core", "status", "--json", "--config", configPath)
		if err == nil && strings.Contains(out, `"state": "running"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("core did not become ready: err=%v out=%s serve=%s", err, out, serveOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	local := filepath.Join(base, "local.txt")
	if err := os.WriteFile(local, []byte("core data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "put", "--wait-timeout", "10s", local, "/local/uploaded.txt"); err != nil {
		t.Fatalf("fs put via core: %v", err)
	}
	if _, err := executeRoot(t, "fs", "--config", configPath, "mv", "/local/uploaded.txt", "/local/moved.txt"); err != nil {
		t.Fatalf("fs mv via core: %v", err)
	}
	statOut, err := executeRoot(t, "fs", "--config", configPath, "stat", "--json", "/local/moved.txt")
	if err != nil {
		t.Fatalf("fs stat via core: %v", err)
	}
	var stat struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(statOut), &stat); err != nil {
		t.Fatalf("decode stat: %v (%s)", err, statOut)
	}
	if stat.Name != "moved.txt" || stat.Size != int64(len("core data")) {
		t.Fatalf("stat = %+v", stat)
	}
	listOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "/local")
	if err != nil {
		t.Fatalf("fs list via core: %v", err)
	}
	if !strings.Contains(listOut, "moved.txt") {
		t.Fatalf("list missing uploaded entry: %s", listOut)
	}
	data, err := os.ReadFile(filepath.Join(remote, "moved.txt"))
	if err != nil {
		t.Fatalf("remote file missing: %v", err)
	}
	if string(data) != "core data" {
		t.Fatalf("remote data = %q", data)
	}

	// The listing was persisted by the Core; an external change is invisible
	// within the TTL and becomes visible after --refresh.
	if err := os.WriteFile(filepath.Join(remote, "external.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cachedOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "/local")
	if err != nil {
		t.Fatalf("cached list via core: %v", err)
	}
	if strings.Contains(cachedOut, "external.txt") {
		t.Fatalf("external change visible before TTL: %s", cachedOut)
	}
	refreshedOut, err := executeRoot(t, "fs", "--config", configPath, "list", "--json", "--refresh", "/local")
	if err != nil {
		t.Fatalf("refresh list via core: %v", err)
	}
	if !strings.Contains(refreshedOut, "external.txt") {
		t.Fatalf("refresh did not refetch: %s", refreshedOut)
	}
}
