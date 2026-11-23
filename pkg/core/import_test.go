package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestCoreFileInfoAndResumeCheck covers the mobile resume contract: the
// identity/size/mod-time snapshots must tell a client whether its partial
// download still matches the remote object.
func TestCoreFileInfoAndResumeCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "a.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	c := newTestCore(t, fs)

	info, err := c.FileInfo(ctx, "/a.bin")
	if err != nil {
		t.Fatalf("file info: %v", err)
	}
	if info.Path != "/a.bin" || info.Size != 10 || info.ModTime == "" {
		t.Fatalf("file info = %+v", info)
	}
	check, err := c.ValidateResume(ctx, "/a.bin", info.ID, info.Size, info.ModTime)
	if err != nil || !check.OK {
		t.Fatalf("matching resume = %+v, %v", check, err)
	}
	check, err = c.ValidateResume(ctx, "/a.bin", "other", info.Size, info.ModTime)
	if err != nil || check.OK || check.Reason != "id_changed" {
		t.Fatalf("id mismatch = %+v, %v", check, err)
	}
	check, err = c.ValidateResume(ctx, "/a.bin", info.ID, info.Size+1, info.ModTime)
	if err != nil || check.OK || check.Reason != "size_changed" {
		t.Fatalf("size mismatch = %+v, %v", check, err)
	}
	check, err = c.ValidateResume(ctx, "/a.bin", info.ID, info.Size, "2000-01-01T00:00:00Z")
	if err != nil || check.OK || check.Reason != "mod_time_changed" {
		t.Fatalf("mod time mismatch = %+v, %v", check, err)
	}
	if _, err := c.FileInfo(ctx, "/missing.bin"); err == nil {
		t.Fatal("file info for missing path succeeded")
	}
}

// TestCoreLogFilesAndReadLog covers the debug log surface, including the
// bounds checks that keep a request from reading past the log limit.
func TestCoreLogFilesAndReadLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "first line\nsecond line\n"
	if err := os.WriteFile(filepath.Join(dir, "qrypt.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newTestCore(t, nil)
	c.runtimeLayout.LogDir = dir
	c.readChunkLimit = 8

	files, err := c.LogFiles()
	if err != nil || len(files) != 1 || files[0].Name != "qrypt.log" || files[0].Size != int64(len(content)) {
		t.Fatalf("log files = %+v, %v", files, err)
	}
	data, err := c.ReadLog("qrypt.log", 0, 8)
	if err != nil || string(data) != content[:8] {
		t.Fatalf("read log = %q, %v", data, err)
	}
	if data, err := c.ReadLog("qrypt.log", 0, 0); err != nil || len(data) != 0 {
		t.Fatalf("zero-length read = %q, %v", data, err)
	}
	if _, err := c.ReadLog("qrypt.log", -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, err := c.ReadLog("qrypt.log", 0, -1); err == nil {
		t.Fatal("negative length accepted")
	}
	if _, err := c.ReadLog("qrypt.log", 0, 9); err == nil {
		t.Fatal("length over the read limit accepted")
	}
	if _, err := c.ReadLog("missing.log", 0, 1); err == nil {
		t.Fatal("missing log accepted")
	}
}

func TestImportedConfigPathRequiresConfigDir(t *testing.T) {
	t.Parallel()
	if _, err := ImportedConfigPath(RuntimeLayout{}); err == nil {
		t.Fatal("empty config dir accepted")
	}
	path, err := ImportedConfigPath(RuntimeLayout{ConfigDir: "/tmp/qrypt-import"})
	if err != nil || path != filepath.Join("/tmp/qrypt-import", ImportedConfigName) {
		t.Fatalf("imported config path = %q, %v", path, err)
	}
	cfg := &config.Config{MountPoint: "/mnt", Logging: config.LoggingConfig{LogFile: "x.log", ErrorFile: "e.log"}}
	sanitizeImportedConfig(cfg)
	if cfg.MountPoint != "" || cfg.Logging.LogFile != "" || cfg.Logging.ErrorFile != "" {
		t.Fatalf("sanitized config = %+v", cfg)
	}
	sanitizeImportedConfig(nil)
}
