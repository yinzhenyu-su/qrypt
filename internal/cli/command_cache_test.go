package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

type cacheClearTestSource struct {
	cleared     []string
	invalidated []string
	faults      []faultinject.DebugReadFault
}

func (s *cacheClearTestSource) DebugSnapshot() diagnostics.DebugSnapshot {
	return diagnostics.DebugSnapshot{SchemaVersion: diagnostics.DebugSnapshotSchemaVersion}
}

func (s *cacheClearTestSource) ClearReadCacheForMount(name string) error {
	s.cleared = append(s.cleared, name)
	return nil
}

func (s *cacheClearTestSource) DebugInvalidateReadCache(_ context.Context, path string) (string, error) {
	s.invalidated = append(s.invalidated, path)
	return "cloud", nil
}

func (s *cacheClearTestSource) DebugInjectReadFault(_ context.Context, req faultinject.DebugReadFaultRequest) (faultinject.DebugReadFault, error) {
	fault := faultinject.DebugReadFault{ID: "read-fault-1", Path: req.Path, Kind: req.Kind, Remaining: 1}
	s.faults = append(s.faults, fault)
	return fault, nil
}

func (s *cacheClearTestSource) DebugClearReadFault(_ context.Context, id string) error {
	if id == "" {
		s.faults = nil
		return nil
	}
	for index, fault := range s.faults {
		if fault.ID == id {
			s.faults = append(s.faults[:index], s.faults[index+1:]...)
			break
		}
	}
	return nil
}

func (s *cacheClearTestSource) DebugReadFaults(context.Context) []faultinject.DebugReadFault {
	return s.faults
}

func TestProbeCacheClearUsesProbeSocket(t *testing.T) {
	workDir := shortTempDir(t)
	configPath := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[storage]\nwork_dir = "+util.TOMLPath(workDir)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	source := &cacheClearTestSource{}
	server, err := control.NewProbeServer(core.ProbeSocketPath(cfg, core.RuntimeLayout{RunDir: core.RuntimeRunDir(configPath, cfg)}), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "cache", "clear", "--mount", "cloud", "--config", configPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("probe cache clear failed: %v", err)
	}
	if len(source.cleared) != 1 || source.cleared[0] != "cloud" {
		t.Fatalf("cleared mounts = %#v, want [cloud]", source.cleared)
	}
	if !strings.Contains(out.String(), `"cleared": true`) {
		t.Fatalf("unexpected cache clear output: %s", out.String())
	}

	out.Reset()
	root = NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "cache", "invalidate", "--path", "/cloud/movie.mp4", "--config", configPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("probe cache invalidate failed: %v", err)
	}
	if len(source.invalidated) != 1 || source.invalidated[0] != "/cloud/movie.mp4" {
		t.Fatalf("invalidated paths = %#v, want [/cloud/movie.mp4]", source.invalidated)
	}
	if !strings.Contains(out.String(), `"invalidated": true`) {
		t.Fatalf("unexpected cache invalidate output: %s", out.String())
	}

	out.Reset()
	root = NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "fault", "read", "--path", "/cloud/movie.mp4", "--kind", "stall", "--after", "1k", "--config", configPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("probe fault read failed: %v", err)
	}
	if len(source.faults) != 1 || source.faults[0].Kind != "stall" {
		t.Fatalf("faults = %#v, want one stall rule", source.faults)
	}
	if !strings.Contains(out.String(), `"id": "read-fault-1"`) {
		t.Fatalf("unexpected fault output: %s", out.String())
	}
	out.Reset()
	root = NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "fault", "list", "--config", configPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("probe fault list failed: %v", err)
	}
	if !strings.Contains(out.String(), `"read-fault-1"`) {
		t.Fatalf("unexpected fault list output: %s", out.String())
	}
	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "fault", "clear", "--id", "read-fault-1", "--config", configPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("probe fault clear failed: %v", err)
	}
	if len(source.faults) != 0 {
		t.Fatalf("faults after clear = %#v, want none", source.faults)
	}
}

func TestProbeCacheClearRequiresMount(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[storage]\nwork_dir = "+util.TOMLPath(filepath.Join(dir, "work"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"probe", "cache", "clear", "--config", configPath})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cache clear requires --mount") {
		t.Fatalf("probe cache clear without mount error = %v", err)
	}
}

// shortTempDir returns a temporary directory with a short absolute path.
// Unix socket paths are capped at ~104 bytes on macOS, while t.TempDir() nests
// the test name under /var/folders/..., so binding probe.sock there fails with
// "invalid argument". Windows endpoints are named pipes, so its temp directory
// is fine as-is.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "qrypt-cache-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
