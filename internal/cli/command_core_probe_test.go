package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCoreServeStartsProbeSocket guards the resident Core's probe surface.
//
// `qrypt probe` talks to the per-config probe.sock. A non-isolated
// `qrypt mount` now attaches to the resident Core instead of hosting the
// debug/probe/control servers itself, so the Core started by `core serve` must
// expose the probe socket too; otherwise every probe command dies with
// "dial unix .../probe.sock: connect: no such file or directory".
func TestCoreServeStartsProbeSocket(t *testing.T) {
	base, err := os.MkdirTemp("", "qrypt-core-probe-")
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

	out, err := executeRoot(t, "probe", "fault", "list", "--config", configPath)
	if err != nil {
		t.Fatalf("probe command through the resident Core: %v (%s)", err, out)
	}
	if !strings.Contains(out, `"faults"`) {
		t.Fatalf("probe fault list = %s, want a fault list payload", out)
	}
}
