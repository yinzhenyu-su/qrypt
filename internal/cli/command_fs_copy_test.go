package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/drivecopy"
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

func TestFsCopyDirJSONIncludesPerEntryManifest(t *testing.T) {
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
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "--recursive", "/src/parent", "/dst", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs copy --recursive --json failed: %v", err)
	}
	var result struct {
		OpID    string `json:"op_id"`
		Pass    bool   `json:"pass"`
		Copied  int    `json:"copied"`
		Skipped int    `json:"skipped"`
		Failed  int    `json:"failed"`
		Entries []struct {
			OpID       string `json:"op_id"`
			Kind       string `json:"kind"`
			State      string `json:"state"`
			SourcePath string `json:"source_path"`
			DestPath   string `json:"dest_path"`
			Bytes      int64  `json:"bytes"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal copy dir json: %v\n%s", err, out.String())
	}
	if result.OpID == "" || !result.Pass || result.Copied != 1 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("unexpected result summary: %+v", result)
	}
	want := map[string]string{
		"directory:/src/parent":      "ready",
		"file:/src/parent/a.txt":     "skipped",
		"directory:/src/parent/sub":  "ready",
		"file:/src/parent/sub/b.txt": "copied",
	}
	for _, entry := range result.Entries {
		key := entry.Kind + ":" + entry.SourcePath
		if want[key] == entry.State {
			delete(want, key)
		}
		if entry.State == "copied" && entry.OpID == "" {
			t.Fatalf("copied entry missing op_id: %+v", entry)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing manifest entries: %#v in %+v", want, result.Entries)
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
[storage]
read_cache_dir = "` + filepath.Join(tmp, "cache", "read") + `"
upload_dir = "` + filepath.Join(tmp, "upload") + `"
state_dir = "` + filepath.Join(tmp, "state") + `"

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

func TestFsCopyDryRunDoesNotCopy(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "--dry-run", "/src/file.txt", "/dst/copied.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run copy failed: %v stderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dstRemote, "copied.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create the destination, stat err = %v", err)
	}
	if !strings.Contains(out.String(), "dry run: would copy 1 files (7 bytes)") {
		t.Fatalf("dry-run summary missing:\n%s", out.String())
	}
}

func TestFsCopyDryRunRecursiveJSON(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(filepath.Join(srcRemote, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "sub", "b.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "copy", "--dry-run", "--recursive", "--json", "/src", "/dst"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run recursive failed: %v stderr=%s", err, stderr.String())
	}
	var plan fsCopyDryRunResult
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("dry-run JSON invalid: %v\n%s", err, out.String())
	}
	if !plan.DryRun || plan.Files != 2 || plan.Bytes != 3 {
		t.Fatalf("plan = %+v, want dry_run, 2 files, 3 bytes", plan)
	}
	joined := strings.Join(plan.Entries, " ")
	if !strings.Contains(joined, "/src/a.txt") || !strings.Contains(joined, "/src/sub/b.txt") {
		t.Fatalf("plan entries = %v, want both files", plan.Entries)
	}
}

func TestFsCopyDirErrorPartialExitCode(t *testing.T) {
	partial := &drivecopy.DriverCopyDirResult{Copied: 3, Failed: 1, Error: "one failed"}
	err := fsCopyDirError(partial)
	var xe *ExitError
	if !errors.As(err, &xe) || xe.Code != ExitPartial {
		t.Fatalf("partial failure err = %v, want ExitError{3}", err)
	}
	total := &drivecopy.DriverCopyDirResult{Copied: 0, Failed: 3, Error: "all failed"}
	if err := fsCopyDirError(total); err == nil {
		t.Fatal("all-failed must return an error")
	} else if errors.As(err, &xe) {
		t.Fatalf("all-failed must not be tagged partial, got %+v", xe)
	}
}

func TestFsListJSONLOutputsOneEntryPerLine(t *testing.T) {
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "b.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFsCopyConfig(t, tmp, srcRemote, dstRemote)

	var out bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"fs", "--config", configPath, "list", "--jsonl", "/src"})
	if err := root.Execute(); err != nil {
		t.Fatalf("list --jsonl failed: %v stderr=%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl output has %d lines, want 2:\n%s", len(lines), out.String())
	}
	var names []string
	for _, line := range lines {
		var entry fsListEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("jsonl line invalid: %v\n%s", err, line)
		}
		names = append(names, entry.Name)
	}
	if names[0] != "a.txt" || names[1] != "b.txt" {
		t.Fatalf("jsonl entries = %v, want [a.txt b.txt]", names)
	}
}

func TestFsListJSONLConflictWithJSONIsUsageError(t *testing.T) {
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

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "list", "--json", "--jsonl", "/src"})
	err := root.Execute()
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitUsage {
		t.Fatalf("err = %v, want ExitError{2}", err)
	}
}
