package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func testRuntimeLayout(tmp string) RuntimeLayout {
	return RuntimeLayout{
		ConfigDir:    filepath.Join(tmp, "runtime", "config"),
		ReadCacheDir: filepath.Join(tmp, "cache", "read"),
		ThumbnailDir: filepath.Join(tmp, "cache", "thumbnail"),
		UploadDir:    filepath.Join(tmp, "runtime", "upload"),
		StateDir:     filepath.Join(tmp, "runtime", "state"),
		LogDir:       filepath.Join(tmp, "runtime", "logs"),
		TmpDir:       filepath.Join(tmp, "cache", "tmp"),
	}
}

func TestBuildFileSystemUsesRuntimeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{
			ReadCacheDir:      filepath.Join(tmp, "desktop-read-cache"),
			ThumbnailCacheDir: filepath.Join(tmp, "desktop-thumbnail-cache"),
			UploadDir:         filepath.Join(tmp, "desktop-upload"),
			StateDir:          filepath.Join(tmp, "desktop-state"),
		},
		Mounts: []config.MountConfig{{
			Name: "quark",
			Type: "localfs",
			Params: config.ParamMap{
				"root_path": remote,
			},
		}},
	}
	runtime := testRuntimeLayout(tmp)
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer stopTestVFS(t, fs)
	fs.Start(ctx)

	if _, err := os.Stat(filepath.Join(runtime.ReadCacheDir, "quark")); err != nil {
		t.Fatalf("runtime read cache not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.UploadDir, "quark", "staging")); err != nil {
		t.Fatalf("runtime upload not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.StateDir, "driver", "quark")); err != nil {
		t.Fatalf("runtime state not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "desktop-read-cache")); !os.IsNotExist(err) {
		t.Fatalf("desktop read cache should not be used, stat err = %v", err)
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
[logging]
log_file = "/desktop/qrypt.log"
error_file = "/desktop/qrypt-error.log"

[storage]
read_cache_dir = "/desktop/cache"
thumbnail_cache_dir = "/desktop/thumbnail"
upload_dir = "/desktop/upload"
state_dir = "/desktop/state"
log_dir = "/desktop/logs"
tmp_dir = "/desktop/tmp"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := testRuntimeLayout(tmp)
	imported, err := ImportConfig(src, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if imported != filepath.Join(runtime.ConfigDir, "qrypt.toml") {
		t.Fatalf("imported path = %q", imported)
	}
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"/Volumes/Qrypt", "/desktop/cache", "/desktop/thumbnail", "/desktop/qrypt.log", "/desktop/upload", "/desktop/state"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("imported config still contains %q:\n%s", forbidden, text)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := OpenImported(ctx, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
}

func TestOpenInitializesRuntimeLog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	runtime := testRuntimeLayout(tmp)
	c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := os.Stat(filepath.Join(runtime.LogDir, "qrypt.log")); err != nil {
		t.Fatalf("runtime log not created: %v", err)
	}
}

func TestStorageUsageAndClearReadCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	runtime := testRuntimeLayout(tmp)
	c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)

	readingFile := filepath.Join(runtime.ReadCacheDir, "quark", "data.batch")
	if err := os.WriteFile(readingFile, []byte("read-cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagingFile := filepath.Join(runtime.UploadDir, "quark", "staging", "data.staging")
	if err := os.WriteFile(stagingFile, []byte("staging"), 0o644); err != nil {
		t.Fatal(err)
	}

	thumbnailFile := filepath.Join(runtime.ThumbnailDir, "thumb.jpg")
	if err := os.WriteFile(thumbnailFile, []byte("thumb"), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, err := c.StorageUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalBytes == 0 || usage.CacheBytes == 0 || usage.ReadCacheBytes != int64(len("read-cache")) || usage.ThumbnailBytes != int64(len("thumb")) || usage.StagingBytes != int64(len("staging")) {
		t.Fatalf("unexpected storage usage: %+v", usage)
	}
	if len(usage.Mounts) != 1 || usage.Mounts[0].Name != "quark" || usage.Mounts[0].ReadCacheBytes != int64(len("read-cache")) {
		t.Fatalf("unexpected mount usage: %+v", usage.Mounts)
	}

	if err := c.ClearReadCache(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(readingFile); !os.IsNotExist(err) {
		t.Fatalf("reading file still exists after clear, err=%v", err)
	}
	if _, err := os.Stat(stagingFile); err != nil {
		t.Fatalf("staging file should remain after clear: %v", err)
	}
}

func TestThumbnailCacheFileLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(remote, "photo.jpg")
	if err := os.WriteFile(source, []byte("source-v1"), 0o644); err != nil {
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
	runtime := testRuntimeLayout(tmp)
	c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)

	miss, err := c.GetThumbnailFile(ctx, "/quark/photo.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if miss.Hit {
		t.Fatalf("initial thumbnail = %+v, want miss", miss)
	}

	localThumb := filepath.Join(tmp, "thumb.jpg")
	if err := os.WriteFile(localThumb, []byte("thumbnail"), 0o644); err != nil {
		t.Fatal(err)
	}
	put, err := c.PutThumbnailFile(ctx, "/quark/photo.jpg", "grid-128", "image/jpeg", localThumb)
	if err != nil {
		t.Fatal(err)
	}
	if !put.Hit || put.Mime != "image/jpeg" || put.Size != int64(len("thumbnail")) {
		t.Fatalf("PutThumbnailFile = %+v", put)
	}
	if data, err := os.ReadFile(put.Path); err != nil || string(data) != "thumbnail" {
		t.Fatalf("cached thumbnail data = %q err=%v", string(data), err)
	}
	hit, err := c.GetThumbnailFile(ctx, "/quark/photo.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Hit || hit.Path != put.Path || hit.Mime != "image/jpeg" {
		t.Fatalf("GetThumbnailFile = %+v, want hit", hit)
	}

	if err := os.WriteFile(source, []byte("source-v2-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	c, err = Open(ctx, Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	changed, err := c.GetThumbnailFile(ctx, "/quark/photo.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hit {
		t.Fatalf("thumbnail after source change = %+v, want miss", changed)
	}

	usage, err := c.ThumbnailCacheUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage == 0 {
		t.Fatal("thumbnail cache usage = 0, want cached bytes")
	}
	if err := c.ClearThumbnailCache(ctx); err != nil {
		t.Fatal(err)
	}
	usage, err = c.ThumbnailCacheUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage != 0 {
		t.Fatalf("thumbnail cache usage after clear = %d, want 0", usage)
	}
}

func TestThumbnailCachePrunesOldEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.jpg"), []byte("source-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "b.jpg"), []byte("source-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
	[thumbnail_cache]
	max_size = "250"

	[[mounts]]
	name = "quark"
	type = "localfs"
	[mounts.params]
	root_path = "`+remote+`"
	`), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := testRuntimeLayout(tmp)
	c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)

	thumbA := filepath.Join(tmp, "a.jpg")
	thumbB := filepath.Join(tmp, "b.jpg")
	if err := os.WriteFile(thumbA, []byte("thumb-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbB, []byte("thumb-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := c.PutThumbnailFile(ctx, "/quark/a.jpg", "grid-128", "image/jpeg", thumbA)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := c.PutThumbnailFile(ctx, "/quark/b.jpg", "grid-128", "image/jpeg", thumbB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("old thumbnail still exists after prune, err=%v", err)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("latest thumbnail should be kept: %v", err)
	}
}

func TestCoreReadAtLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{Runtime: testRuntimeLayout(tmp)})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer stopTestVFS(t, fs)
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
	buf := []byte("xxxxx")
	n, err := c.ReadAtInto(ctx, "/quark/file.txt", 3, buf, 8)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || string(buf[:n]) != "lo" {
		t.Fatalf("ReadAtInto n=%d data=%q, want 2 lo", n, string(buf[:n]))
	}
	if _, err := c.ReadAtInto(ctx, "/quark/file.txt", 0, make([]byte, 5), 4); err == nil {
		t.Fatal("expected ReadAtInto limit error")
	}
}

func TestCoreCRUDUsesVFSStaging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		UploadDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	t.Cleanup(func() {
		cancel()
		_ = fs.CloseReadCache()
	})
	c := newTestCore(t, fs)

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
	waitVFSIdle(t, fs)

	if err := c.Rename(ctx, "/docs/file.txt", "/docs/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoreCondition(t, func() bool {
		data, err := os.ReadFile(filepath.Join(remote, "docs", "renamed.txt"))
		return err == nil && string(data) == "hello from staging"
	})
	waitVFSIdle(t, fs)
}

func waitVFSIdle(t *testing.T, fs *vfs.VFS) {
	t.Helper()
	waitCoreCondition(t, func() bool {
		snapshot := fs.DebugSnapshot()
		return len(fs.PendingUploads()) == 0 &&
			len(snapshot.Mounts) == 1 &&
			len(snapshot.Mounts[0].ActiveUploads()) == 0
	})
}

func waitCoreCondition(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
