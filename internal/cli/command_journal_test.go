package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestPrintPendingVerboseIncludesDebugState(t *testing.T) {
	tmp := t.TempDir()
	localPath := filepath.Join(tmp, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printPendingVerbose(&out, []vfs.PendingUpload{{
		Path:       "/file.txt",
		Size:       4,
		LocalPath:  localPath,
		RetryCount: 1,
		LastError:  "boom",
	}})
	text := out.String()
	for _, want := range []string{"/file.txt", "size-mismatch(3)", "boom", "RETRY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected verbose pending output to contain %q, got:\n%s", want, text)
		}
	}
}

func waitPendingEmpty(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := pendingFiles(fs)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	pending, err := pendingFiles(fs)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("pending uploads did not drain: %+v", pending)
}

func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() diagnostics.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.ActiveUploads())
	}
	return count
}

func waitPathMissing(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = os.Stat(path)
		if os.IsNotExist(lastErr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("path still exists: %s err=%v", path, lastErr)
}

func setupJournalCache(t *testing.T) string {
	t.Helper()
	cache := filepath.Join(t.TempDir(), "cache")
	staging := filepath.Join(cache, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(cache, "pending.jsonl")
	f, err := os.OpenFile(journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries := []string{
		`{"op":"dirty","path":"/keep.txt","fid":"f-keep","local_path":` + strconv.Quote(filepath.Join(staging, "keep")) + `,"size":4}`,
		`{"op":"dirty","path":"/fail.txt","fid":"f-fail","local_path":` + strconv.Quote(filepath.Join(staging, "fail")) + `,"size":4,"permanent_fail":true,"last_error":"network","retry_count":2}`,
		`{"op":"dirty","path":"/gone.txt","fid":"f-gone","local_path":` + strconv.Quote(filepath.Join(staging, "gone")) + `,"size":4}`,
	}
	for _, line := range entries {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "keep"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "fail"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cache
}

func TestJournalReplayResetsFailedUploads(t *testing.T) {
	cache := setupJournalCache(t)
	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "journal", "--cache-dir", cache, "replay", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("journal replay failed: %v", err)
	}
	var results []journalMaintenanceResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("replay JSON invalid: %v\n%s", err, out.String())
	}
	if len(results) != 1 || results[0].Replayed != 1 {
		t.Fatalf("results = %+v, want 1 replayed", results)
	}
	if len(results[0].Entries) != 1 || results[0].Entries[0] != "/fail.txt" {
		t.Fatalf("replayed entries = %v, want [/fail.txt]", results[0].Entries)
	}
	// Journal must no longer carry failure state for /fail.txt.
	data, err := os.ReadFile(filepath.Join(cache, "pending.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "permanent_fail") {
		t.Fatalf("journal still holds permanent_fail after replay:\n%s", data)
	}
}

func TestJournalPruneDropsMissingStaging(t *testing.T) {
	cache := setupJournalCache(t)
	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "journal", "--cache-dir", cache, "prune", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("journal prune failed: %v", err)
	}
	var results []journalMaintenanceResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("prune JSON invalid: %v\n%s", err, out.String())
	}
	if len(results) != 1 || results[0].Pruned != 1 {
		t.Fatalf("results = %+v, want 1 pruned", results)
	}
	data, err := os.ReadFile(filepath.Join(cache, "pending.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/gone.txt") {
		t.Fatalf("journal still holds /gone.txt after prune:\n%s", data)
	}
	if !strings.Contains(string(data), "/keep.txt") || !strings.Contains(string(data), "/fail.txt") {
		t.Fatalf("prune dropped live entries:\n%s", data)
	}
}
