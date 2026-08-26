package cli

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildNamespaceRejectsInvalidDeleteDelay(t *testing.T) {
	ctx := context.Background()
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

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
[mounts.upload]
delete_delay = "soon"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := buildFileSystem(ctx, configPath)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "upload.delete_delay") {
		t.Fatalf("expected upload.delete_delay error, got %v", err)
	}
}

func TestBuildFileSystemRejectsInvalidBandwidth(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
[bandwidth]
download = "fast"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := buildFileSystem(ctx, configPath)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "bandwidth.download") {
		t.Fatalf("expected bandwidth.download error, got %v", err)
	}
}
