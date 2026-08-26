package cli

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildNamespaceDoesNotLimitLocalStagingUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[bandwidth]
upload = "1"

[upload]
upload_delay = "10ms"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/encrypted/slow.txt", []byte("slow upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/encrypted/slow.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
}

func TestBuildNamespaceUsesStorageDirs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(cacheDir, "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[upload]
upload_delay = "10ms"

[[mounts]]
name = "one"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remoteA)+`

[[mounts]]
name = "two"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remoteB)+`
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
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

	if _, err := os.Stat(filepath.Join(cacheDir, "read", "one")); err != nil {
		t.Fatalf("expected read cache dir for mount one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "read", "two")); err != nil {
		t.Fatalf("expected read cache dir for mount two: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "upload", "one", "staging")); err != nil {
		t.Fatalf("expected upload dir for mount one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "upload", "two", "staging")); err != nil {
		t.Fatalf("expected upload dir for mount two: %v", err)
	}
}
