package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

[upload]
upload_delay = "10ms"
delete_delay = "10ms"

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

func TestCoreEndpointPrintsConfigurationEndpoint(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-endpoint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)

	out, err := executeRoot(t, "core", "endpoint", "--json", "--config", configPath)
	if err != nil {
		t.Fatalf("core endpoint --json: %v (%s)", err, out)
	}
	var report struct {
		Endpoint        string `json:"endpoint"`
		Network         string `json:"network"`
		RunDir          string `json:"run_dir"`
		Running         bool   `json:"running"`
		PID             int    `json:"pid"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode endpoint json: %v (%s)", err, out)
	}
	if report.Endpoint == "" || !strings.Contains(report.Endpoint, "control") {
		t.Fatalf("endpoint = %q", report.Endpoint)
	}
	if report.Network != "unix" {
		t.Fatalf("network = %q", report.Network)
	}
	if report.RunDir == "" {
		t.Fatal("expected run_dir")
	}
	if report.Running {
		t.Fatalf("expected running=false with no Core started (pid %d)", report.PID)
	}

	plain, err := executeRoot(t, "core", "endpoint", "--config", configPath)
	if err != nil {
		t.Fatalf("core endpoint: %v (%s)", err, plain)
	}
	if !strings.Contains(plain, "endpoint: "+report.Endpoint) {
		t.Fatalf("plain output missing endpoint: %s", plain)
	}
	if !strings.Contains(plain, "running: false") {
		t.Fatalf("plain output missing running state: %s", plain)
	}
}

func TestCoreServeStatusStop(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, workDir := writeCoreServeTestConfig(t, base)

	serveRoot := NewRootCommand()
	serveOut := &lockedServeBuffer{}
	serveRoot.SetOut(serveOut)
	serveRoot.SetErr(serveOut)
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

	// The foreground serve must explain that it is up: the runtime logger writes
	// to files, so without this line the terminal stays silent (#46).
	readyDeadline := time.Now().Add(10 * time.Second)
	for {
		text := serveOut.String()
		if strings.Contains(text, "Core ready") && strings.Contains(text, "qrypt.log") {
			if !strings.Contains(text, "pid "+strconv.Itoa(os.Getpid())) {
				t.Fatalf("serve ready line missing pid: %s", text)
			}
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("serve did not print a ready line: %q", text)
		}
		time.Sleep(20 * time.Millisecond)
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

func TestCoreServeIdleExit(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-idle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, workDir := writeCoreServeTestConfig(t, base)

	root := NewRootCommand()
	out := &lockedServeBuffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"core", "serve", "--foreground", "--idle-timeout", "300ms", "--config", configPath})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	t.Cleanup(func() { _, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath) })

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle serve exited with error: %v output=%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not idle-exit: %s", out.String())
	}
	if held, _ := ownerlock.Peek(workDir); held {
		t.Fatal("owner lock still held after idle exit")
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

func TestUnmountWithoutCore(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-unmount-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)

	out, err := executeRoot(t, "unmount", "--config", configPath)
	if err == nil {
		t.Fatalf("unmount without a core succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "no running Core") {
		t.Fatalf("unmount error = %v, want clue about a missing Core", err)
	}
}

func TestUnmountWithCoreNoMounts(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-unmount-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)

	serveRoot := NewRootCommand()
	var serveOut bytes.Buffer
	serveRoot.SetOut(&serveOut)
	serveRoot.SetErr(&serveOut)
	serveRoot.SetArgs([]string{"core", "serve", "--foreground", "--keep", "--config", configPath})
	done := make(chan error, 1)
	go func() { done <- serveRoot.Execute() }()
	t.Cleanup(func() {
		_, _ = executeRoot(t, "core", "stop", "--force", "--config", configPath)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := executeRoot(t, "core", "status", "--json", "--config", configPath)
		if err == nil && strings.Contains(out, `"state": "running"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("core did not become ready: err=%v output=%s serve=%s", err, out, serveOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	out, err := executeRoot(t, "unmount", "--config", configPath)
	if err != nil {
		t.Fatalf("unmount: %v", err)
	}
	if !strings.Contains(out, "no mounts are active") {
		t.Fatalf("unmount output = %s, want no mounts message", out)
	}
}

// lockedServeBuffer collects foreground serve output without racing the serve
// goroutine that the test reads it from.
type lockedServeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedServeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedServeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
