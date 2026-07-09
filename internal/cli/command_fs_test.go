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

func TestFSGetDirRecursive(t *testing.T) {
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

	// Create nested remote directory structure.
	if err := runMkdir(testCommand(), []string{"/local/parent"}); err != nil {
		t.Fatal(err)
	}
	if err := runMkdir(testCommand(), []string{"/local/parent/sub"}); err != nil {
		t.Fatal(err)
	}

	// Write files at each level directly to the remote backing store.
	if err := os.WriteFile(filepath.Join(remote, "parent", "a.txt"), []byte("file-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "parent", "sub", "b.txt"), []byte("file-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "parent", "sub", "c.txt"), []byte("file-c"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Download the whole directory recursively.
	// LOCAL is treated as the parent directory; the remote dir name is appended.
	downloadParent := filepath.Join(tmp, "dl")
	if err := runGet(testCommand(), []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir: %v", err)
	}

	checkFile := func(path, want string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %q: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("%q: got %q, want %q", path, data, want)
		}
	}

	// Remote dir "parent" is placed inside LOCAL.
	base := filepath.Join(downloadParent, "parent")
	checkFile(filepath.Join(base, "a.txt"), "file-a")
	checkFile(filepath.Join(base, "sub", "b.txt"), "file-b")
	checkFile(filepath.Join(base, "sub", "c.txt"), "file-c")

	// Without --force, existing files are skipped (not overwritten, not error).
	// Modify a local file, then re-download into the same parent.
	unwantedContent := []byte("UNWANTED")
	if err := os.WriteFile(filepath.Join(base, "a.txt"), unwantedContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGet(testCommand(), []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir skip existing: %v", err)
	}
	// a.txt should still have the unwanted content (was not overwritten).
	data, err := os.ReadFile(filepath.Join(base, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(unwantedContent) {
		t.Fatalf("expected skip: got %q, want %q", data, unwantedContent)
	}

	// With --force, existing files are overwritten.
	forceGet := testCommand()
	forceGet.Flags().Bool("force", true, "")
	if err := runGet(forceGet, []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir force: %v", err)
	}
	checkFile(filepath.Join(base, "a.txt"), "file-a")

	// Downloading a directory to an existing file path should error.
	filePath := filepath.Join(tmp, "blocker.txt")
	if err := os.WriteFile(filePath, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGet(testCommand(), []string{"/local/parent", filePath}); err == nil {
		t.Fatal("expected error when local exists as a file")
	}
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
