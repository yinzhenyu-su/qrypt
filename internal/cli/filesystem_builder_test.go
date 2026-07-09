package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
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
