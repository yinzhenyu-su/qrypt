package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/config"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

func TestBuildFileSystemUsesWorkDirCache(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		CacheDir: filepath.Join(tmp, "desktop-cache"),
		Mounts: []config.MountConfig{{
			Name: "quark",
			Type: "localfs",
			Params: config.ParamMap{
				"root_path": remote,
			},
			Cache: &config.CacheConfig{
				Dir: filepath.Join(tmp, "mount-cache"),
			},
		}},
	}
	workDir := filepath.Join(tmp, "work")
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := os.Stat(filepath.Join(workDir, "cache", "quark", "reading")); err != nil {
		t.Fatalf("work dir cache not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "state", "quark", "driver")); err != nil {
		t.Fatalf("work dir state not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "desktop-cache")); !os.IsNotExist(err) {
		t.Fatalf("desktop cache should not be used, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "mount-cache")); !os.IsNotExist(err) {
		t.Fatalf("mount cache should not be used, stat err = %v", err)
	}
}

func TestOpenInitializesWorkDirLog(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(tmp, "work")
	c, err := Open(ctx, Options{ConfigPath: configPath, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := os.Stat(filepath.Join(workDir, "logs", "qrypt.log")); err != nil {
		t.Fatalf("work dir log not created: %v", err)
	}
}

func TestCoreReadAtLimit(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Mounts: []config.MountConfig{{
			Name: "quark",
			Type: "localfs",
			Params: config.ParamMap{
				"root_path": remote,
			},
		}},
	}
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{WorkDir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)
	c := &Core{fs: fs, cleanup: cleanup}
	if _, err := c.ReadAt(ctx, "/quark/file.txt", 0, 5, 4); err == nil {
		t.Fatal("expected limit error")
	}
	data, err := c.ReadAt(ctx, "/quark/file.txt", 1, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ell" {
		t.Fatalf("ReadAt = %q, want ell", string(data))
	}
}
