package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
)

func testCommand() *cobra.Command {
	return &cobra.Command{Use: "test"}
}

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
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "quark2"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
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

func TestBuildFileSystemSelectsSingleMount(t *testing.T) {
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
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "quark-test"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	fs, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "quark-test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("single mount root entries = %+v, want empty remote root", entries)
	}

	if _, err := fs.WriteAt(ctx, "/test.txt", []byte("selected"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
	data, err := os.ReadFile(filepath.Join(remoteB, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "selected" {
		t.Fatalf("unexpected selected remote data: %q", data)
	}
	if _, err := os.Stat(filepath.Join(remoteA, "test.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected remote should not receive file, stat err = %v", err)
	}
}

func TestBuildFileSystemSelectsMissingMount(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "missing")
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), `mount "missing" not found`) {
		t.Fatalf("error = %v, want missing mount error", err)
	}
}
