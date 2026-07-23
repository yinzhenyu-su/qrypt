package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
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

func TestImportConfigSanitizesRuntimePaths(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "desktop.toml")
	if err := os.WriteFile(src, []byte(`
mount_point = "/Volumes/Qrypt"
cache_dir = "/desktop/cache"

[logging]
log_file = "/desktop/qrypt.log"
error_file = "/desktop/qrypt-error.log"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
[mounts.cache]
dir = "/desktop/mount-cache"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(tmp, "work")
	imported, err := ImportConfig(src, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if imported != filepath.Join(workDir, "config", "qrypt.toml") {
		t.Fatalf("imported path = %q", imported)
	}
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"/Volumes/Qrypt", "/desktop/cache", "/desktop/qrypt.log", "/desktop/mount-cache"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("imported config still contains %q:\n%s", forbidden, text)
		}
	}
	c, err := OpenImported(context.Background(), workDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
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

func TestCoreCRUDUsesVFSStaging(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		CacheDir:    filepath.Join(tmp, "cache"),
		UploadDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	dir, err := c.Mkdir(ctx, "/docs")
	if err != nil {
		t.Fatal(err)
	}
	if !dir.IsDir || dir.Name != "docs" {
		t.Fatalf("mkdir entry = %+v", dir)
	}

	localPath := filepath.Join(tmp, "local.txt")
	if err := os.WriteFile(localPath, []byte("hello from staging"), 0o644); err != nil {
		t.Fatal(err)
	}
	uploaded, err := c.UploadLocalFile(ctx, localPath, "/docs/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.Name != "file.txt" || uploaded.Size != int64(len("hello from staging")) {
		t.Fatalf("uploaded entry = %+v", uploaded)
	}
	waitCoreCondition(t, func() bool {
		data, err := os.ReadFile(filepath.Join(remote, "docs", "file.txt"))
		return err == nil && string(data) == "hello from staging"
	})

	if err := c.Rename(ctx, "/docs/file.txt", "/docs/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoreCondition(t, func() bool {
		data, err := os.ReadFile(filepath.Join(remote, "docs", "renamed.txt"))
		return err == nil && string(data) == "hello from staging"
	})
}

func waitCoreCondition(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
