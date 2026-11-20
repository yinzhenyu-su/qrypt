package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/core/ownerlock"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func writeCoreServeTestConfig(t *testing.T, base string) (configPath, workDir string) {
	t.Helper()
	remote := filepath.Join(base, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir = filepath.Join(base, "work")
	configPath = filepath.Join(base, "qrypt.toml")
	content := `
mount_point = ` + util.TOMLPath(filepath.Join(base, "mnt")) + `
[storage]
work_dir = ` + util.TOMLPath(workDir) + `

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = ` + util.TOMLPath(remote) + `
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, workDir
}

func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestCoreServeStatusStop(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, workDir := writeCoreServeTestConfig(t, base)

	serveRoot := NewRootCommand()
	var serveOut bytes.Buffer
	serveRoot.SetOut(&serveOut)
	serveRoot.SetErr(&serveOut)
	serveRoot.SetArgs([]string{"core", "serve", "--foreground", "--keep", "--config", configPath})
	done := make(chan error, 1)
	go func() { done <- serveRoot.Execute() }()

	stopServe := func() {
		select {
		case <-done:
			return
		default:
		}
		_, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	t.Cleanup(stopServe)

	deadline := time.Now().Add(10 * time.Second)
	var status string
	for {
		out, err := executeRoot(t, "core", "status", "--json", "--config", configPath)
		if err == nil && strings.Contains(out, `"state": "running"`) {
			status = out
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("core did not become ready: err=%v output=%s serve=%s", err, out, serveOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(status, `"pid": `+strconv.Itoa(os.Getpid())) {
		t.Fatalf("status pid does not reference the serving process: %s", status)
	}

	held, meta := ownerlock.Peek(workDir)
	if !held {
		t.Fatalf("owner lock not held while core is running")
	}
	if meta.PID != os.Getpid() {
		t.Fatalf("owner meta pid = %d, want %d", meta.PID, os.Getpid())
	}

	if _, err := executeRoot(t, "core", "stop", "--config", configPath); err != nil {
		t.Fatalf("core stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("core serve returned error: %v output=%s", err, serveOut.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("core serve did not exit after stop: %s", serveOut.String())
	}
	if held, _ := ownerlock.Peek(workDir); held {
		t.Fatal("owner lock still held after core stopped")
	}
}

func TestCoreStatusStoppedWithoutCore(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)

	out, err := executeRoot(t, "core", "status", "--config", configPath)
	if err != nil {
		t.Fatalf("core status: %v", err)
	}
	if !strings.Contains(out, "stopped") {
		t.Fatalf("status output = %s, want stopped", out)
	}
}
