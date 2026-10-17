package core

import (
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

func TestRuntimeRunDirIsolatedByConfigPath(t *testing.T) {
	t.Setenv("QRYPT_HOME", "")
	cfg := &config.Config{}
	dir := t.TempDir()
	first := RuntimeRunDir(filepath.Join(dir, "one.toml"), cfg)
	second := RuntimeRunDir(filepath.Join(dir, "two.toml"), cfg)
	if first == second {
		t.Fatalf("config runtime directories collided: %q", first)
	}
	if filepath.Base(filepath.Dir(first)) != "run" || filepath.Base(filepath.Dir(second)) != "run" {
		t.Fatalf("unexpected runtime directories: %q, %q", first, second)
	}
	if got := DebugSocketPath(cfg, RuntimeLayout{RunDir: first}); got != filepath.Join(first, "debug.sock") {
		t.Fatalf("debug socket = %q", got)
	}
	if got := ControlSocketPath(cfg, RuntimeLayout{RunDir: first}); got != filepath.Join(first, "control.sock") {
		t.Fatalf("control socket = %q", got)
	}
}

func TestRuntimeRunDirHonorsStorageAndHomeOverrides(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "storage")
	if got := RuntimeRunDir("/tmp/config.toml", &config.Config{Storage: config.StorageConfig{WorkDir: storage}}); got != filepath.Join(storage, "run") {
		t.Fatalf("storage runtime directory = %q", got)
	}
	home := filepath.Join(t.TempDir(), "qrypt-home")
	t.Setenv("QRYPT_HOME", home)
	if got := RuntimeRunDir("/tmp/config.toml", &config.Config{}); got != filepath.Join(home, "run") {
		t.Fatalf("home runtime directory = %q", got)
	}
}

func TestRuntimeEndpointUsesNamedPipeOnWindows(t *testing.T) {
	got := runtimeEndpointForOS(`C:\Users\test\qrypt\run\abc123`, "control", "windows")
	if got != `pipe:\\.\pipe\qrypt-d723a13b1dc1-control` {
		t.Fatalf("Windows endpoint = %q", got)
	}
}
