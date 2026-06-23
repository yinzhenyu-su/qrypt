package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestBuildFileSystemCreatesNamespaceFromMountConfig(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root = "`+remoteA+`"

[[mounts]]
name = "quark2"
type = "localfs"
[mounts.params]
root = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	fs, cleanup, err := buildFileSystem(ctx, flags, "localfs", "", filepath.Join(tmp, "cache"), configPath, "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "quark" || entries[1].Name != "quark2" {
		t.Fatalf("unexpected namespace entries: %+v", entries)
	}

	if _, err := fs.WriteAt(ctx, "/quark2/test.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/quark2/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
	data, err := os.ReadFile(filepath.Join(remoteB, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Fatalf("unexpected remote data: %q", data)
	}
}

func TestBuildNamespaceUsesPerMountEncryption(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	plainRemote := filepath.Join(tmp, "plain")
	encryptedRemote := filepath.Join(tmp, "encrypted")
	if err := os.MkdirAll(plainRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(encryptedRemote, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root = "`+plainRemote+`"
[mounts.encryption]
password = "plain-pass"
salt = "plain-salt"
filename_encryption = "off"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root = "`+encryptedRemote+`"
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	fs, cleanup, err := buildFileSystem(ctx, flags, "localfs", "", filepath.Join(tmp, "cache"), configPath, "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/plain/same.txt", []byte("plain mount"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/plain/same.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/encrypted/same.txt", []byte("encrypted mount"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/encrypted/same.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	if _, err := os.Stat(filepath.Join(plainRemote, "same.txt")); err != nil {
		t.Fatalf("plain mount should keep plaintext filename: %v", err)
	}
	encryptedNames, err := os.ReadDir(encryptedRemote)
	if err != nil {
		t.Fatal(err)
	}
	if len(encryptedNames) != 1 {
		t.Fatalf("expected one encrypted file, got %d", len(encryptedNames))
	}
	if encryptedNames[0].Name() == "same.txt" || strings.Contains(encryptedNames[0].Name(), "same") {
		t.Fatalf("encrypted mount used plaintext filename: %q", encryptedNames[0].Name())
	}
}

func TestBuildNamespaceUsesTopLevelCacheDir(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	cacheDir := filepath.Join(tmp, "configured-cache")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+cacheDir+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "one"
type = "localfs"
[mounts.params]
root = "`+remoteA+`"

[[mounts]]
name = "two"
type = "localfs"
[mounts.params]
root = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	fs, cleanup, err := buildFileSystem(ctx, flags, "localfs", "", "", configPath, "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/two/cache.txt", []byte("uses configured cache"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/two/cache.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	if _, err := os.Stat(filepath.Join(cacheDir, "one", "staging")); err != nil {
		t.Fatalf("expected cache dir for mount one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "two", "staging")); err != nil {
		t.Fatalf("expected cache dir for mount two: %v", err)
	}
}

func TestLoggingFromConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
[logging]
fuse_trace = true
fuse_trace_file = "`+filepath.Join(tmp, "fuse.log")+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	logging, err := loggingFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !logging.FuseTrace {
		t.Fatal("expected fuse trace to be enabled")
	}
	if logging.FuseTraceFile != filepath.Join(tmp, "fuse.log") {
		t.Fatalf("unexpected fuse trace file: %q", logging.FuseTraceFile)
	}
}

func TestMountConfigFromConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
volume_name = "Qrypt Dev"
no_apple_double = false
total_space = "2T"
free_space = "1.5T"

[logging]
fuse_trace = true
fuse_trace_file = "`+filepath.Join(tmp, "fuse.log")+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	mountConfig, err := mountConfigFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt Dev" {
		t.Fatalf("unexpected volume name: %q", mountConfig.VolumeName)
	}
	if mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to be disabled")
	}
	if mountConfig.TotalSpace != 2<<40 {
		t.Fatalf("unexpected total space: %d", mountConfig.TotalSpace)
	}
	if mountConfig.FreeSpace != 1536<<30 {
		t.Fatalf("unexpected free space: %d", mountConfig.FreeSpace)
	}
	if !mountConfig.Logging.FuseTrace {
		t.Fatal("expected fuse trace to be enabled")
	}
	if mountConfig.Logging.FuseTraceFile != filepath.Join(tmp, "fuse.log") {
		t.Fatalf("unexpected fuse trace file: %q", mountConfig.Logging.FuseTraceFile)
	}
}

func TestMountConfigFromConfigDefaults(t *testing.T) {
	mountConfig, err := mountConfigFromConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt" {
		t.Fatalf("unexpected default volume name: %q", mountConfig.VolumeName)
	}
	if !mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to default to true")
	}
}

func waitPendingEmpty(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}
