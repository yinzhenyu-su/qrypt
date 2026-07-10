package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFsCopyCopiesBetweenMounts(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("copy payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/file.txt", "/dst/copied.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy failed: %v stderr=%s", err, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(dstRemote, "copied.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "copy payload" {
		t.Fatalf("copied payload = %q, want copy payload", got)
	}
	if !strings.Contains(out.String(), "copied /src/file.txt -> /dst/copied.txt") {
		t.Fatalf("summary missing copy line:\n%s", out.String())
	}
}

func TestFsCopyRequiresForceForExistingDestination(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("new payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRemote, "copied.txt"), []byte("old payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/file.txt", "/dst/copied.txt"})
	if err := root.Execute(); err == nil {
		t.Fatal("fs copy without --force succeeded, want failure")
	}
	got, err := os.ReadFile(filepath.Join(dstRemote, "copied.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old payload" {
		t.Fatalf("destination changed without overwrite: %q", got)
	}

	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/file.txt", "/dst/copied.txt", "--force"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy --force failed: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dstRemote, "copied.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new payload" {
		t.Fatalf("overwritten payload = %q, want new payload", got)
	}
}

func TestFsCopyAcceptsDeprecatedOverwriteAlias(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("new payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRemote, "copied.txt"), []byte("old payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/file.txt", "/dst/copied.txt", "--overwrite"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy --overwrite failed: %v", err)
	}
	checkLocalFile(t, filepath.Join(dstRemote, "copied.txt"), "new payload")
}

func TestFsCopyJSONFailureReturnsError(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/missing.txt", "/dst/copied.txt", "--json"})
	if err := root.Execute(); err == nil {
		t.Fatal("fs copy --json missing source succeeded, want failure")
	}
	if !strings.Contains(out.String(), `"pass": false`) {
		t.Fatalf("json failure output missing pass=false:\n%s", out.String())
	}
}

func TestFsCopyDirRequiresRecursive(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(filepath.Join(srcRemote, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "/src/parent", "/dst"})
	if err := root.Execute(); err == nil {
		t.Fatal("fs copy directory without --recursive succeeded, want failure")
	}
}

func TestFsCopyDirRecursiveSkipsExistingFiles(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(filepath.Join(srcRemote, "parent", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstRemote, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "parent", "a.txt"), []byte("file-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "parent", "sub", "b.txt"), []byte("file-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRemote, "parent", "a.txt"), []byte("existing-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "--recursive", "/src/parent", "/dst"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy --recursive failed: %v", err)
	}
	checkLocalFile(t, filepath.Join(dstRemote, "parent", "a.txt"), "existing-a")
	checkLocalFile(t, filepath.Join(dstRemote, "parent", "sub", "b.txt"), "file-b")
	for _, want := range []string{
		"copied directory /src/parent -> /dst/parent",
		"files copied: 1",
		"files skipped: 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary missing %q:\n%s", want, out.String())
		}
	}
}

func TestFsCopyDirRecursiveOverwritesExistingFiles(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(filepath.Join(srcRemote, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstRemote, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "parent", "a.txt"), []byte("new-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRemote, "parent", "a.txt"), []byte("old-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "-r", "/src/parent", "/dst", "--force"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy -r --force failed: %v", err)
	}
	checkLocalFile(t, filepath.Join(dstRemote, "parent", "a.txt"), "new-a")
	if !strings.Contains(out.String(), "files copied: 1") {
		t.Fatalf("summary missing copied count:\n%s", out.String())
	}
}

func TestFsCopyDirRecursiveCreatesDestinationParents(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(filepath.Join(srcRemote, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "parent", "a.txt"), []byte("file-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "-r", "/src/parent", "/dst/new/place"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy -r with missing destination parents failed: %v", err)
	}
	checkLocalFile(t, filepath.Join(dstRemote, "new", "place", "parent", "a.txt"), "file-a")
}

func writeFsCopyConfig(t *testing.T, tmp, srcRemote, dstRemote string) string {
	t.Helper()
	configPath := filepath.Join(tmp, "qrypt.toml")
	content := `
mount_point = "` + filepath.Join(tmp, "mnt") + `"
cache_dir = "` + filepath.Join(tmp, "cache") + `"

[[mounts]]
name = "src"
type = "localfs"
[mounts.params]
root_path = "` + srcRemote + `"

[[mounts]]
name = "dst"
type = "localfs"
[mounts.params]
root_path = "` + dstRemote + `"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func checkLocalFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%q = %q, want %q", path, data, want)
	}
}
