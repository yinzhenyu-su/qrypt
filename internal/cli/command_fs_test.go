package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestFSCommandsCreateMoveAndRemoveLocalFS(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
delete_delay = "10ms"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	if err := runMkdir(testCommand(), []string{"/local/dir"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "dir")); err != nil {
		t.Fatalf("mkdir did not create remote dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	downloadPath := filepath.Join(tmp, "download.txt")
	if err := runGet(testCommand(), []string{"/local/dir/file.txt", downloadPath}); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != "data" {
		t.Fatalf("unexpected downloaded content: %q", downloaded)
	}
	if err := runGet(testCommand(), []string{"/local/dir/file.txt", downloadPath}); err == nil {
		t.Fatal("expected get to reject an existing local destination")
	}
	forceGet := testCommand()
	forceGet.Flags().Bool("force", true, "")
	if err := runGet(forceGet, []string{"/local/dir/file.txt", downloadPath}); err != nil {
		t.Fatalf("forced get: %v", err)
	}
	stdinPut := testCommand()
	stdinPut.SetIn(strings.NewReader("from stdin"))
	if err := runPut(stdinPut, []string{"-", "/local/dir/stdin.txt"}); err != nil {
		t.Fatalf("stdin put: %v", err)
	}
	stdinData, err := os.ReadFile(filepath.Join(remote, "dir", "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdinData) != "from stdin" {
		t.Fatalf("unexpected stdin upload: %q", stdinData)
	}
	if err := runMv(testCommand(), []string{"/local/dir/file.txt", "/local/dir/renamed.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "dir", "renamed.txt")); err != nil {
		t.Fatalf("mv did not rename remote file: %v", err)
	}
	if err := runRm(testCommand(), []string{"/local/dir/renamed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := runRm(testCommand(), []string{"/local/dir/stdin.txt"}); err != nil {
		t.Fatal(err)
	}
	waitPathMissing(t, filepath.Join(remote, "dir", "renamed.txt"))
	if err := runRm(testCommand(), []string{"/local/dir"}); err != nil {
		t.Fatal(err)
	}
	waitPathMissing(t, filepath.Join(remote, "dir"))
}

func TestPrintEntryStat(t *testing.T) {
	var out bytes.Buffer
	printEntryStat(&out, drive.Entry{
		ID:       "id",
		ParentID: "parent",
		Name:     "file.txt",
		Size:     12,
		ModTime:  time.Unix(123, 0).UTC(),
	})
	text := out.String()
	for _, want := range []string{"type: file", "name: file.txt", "id: id", "parent_id: parent", "size: 12", "mod_time: 1970-01-01T00:02:03Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stat output missing %q:\n%s", want, text)
		}
	}
}
