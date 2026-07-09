package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestInspectJournalCacheReportsPendingProblems(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	stagingDir := filepath.Join(cacheDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(stagingDir, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(stagingDir, "orphan.staging")
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(cacheDir, "pending.jsonl")
	dirty, err := json.Marshal(struct {
		Op string `json:"op"`
		vfs.PendingFile
	}{
		Op: "dirty",
		PendingFile: vfs.PendingFile{
			Path:       "/file.txt",
			FID:        "file",
			Name:       "file.txt",
			LocalPath:  localPath,
			Size:       4,
			RetryCount: 2,
			LastError:  "upload failed",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clean, err := json.Marshal(struct {
		Op string `json:"op"`
		vfs.PendingFile
	}{
		Op:          "clean",
		PendingFile: vfs.PendingFile{Path: "/old.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(dirty) + "\n" + string(clean) + "\n" + "{bad json\n"
	if err := os.WriteFile(journalPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	report := inspectJournalCache(debugCacheTarget{Name: "test", Dir: cacheDir})
	if report.Entries != 2 || report.DirtyEntries != 1 || report.CleanEntries != 1 {
		t.Fatalf("unexpected journal counts: %+v", report)
	}
	if len(report.InvalidEntries) != 1 {
		t.Fatalf("expected one invalid entry, got %+v", report.InvalidEntries)
	}
	if len(report.Pending) != 1 {
		t.Fatalf("expected one pending entry, got %+v", report.Pending)
	}
	if !report.Pending[0].StagingExists || report.Pending[0].StagingSize != 3 {
		t.Fatalf("expected staging size mismatch details, got %+v", report.Pending[0])
	}
	if len(report.OrphanStaging) != 1 || report.OrphanStaging[0] != orphanPath {
		t.Fatalf("unexpected orphan staging files: %+v", report.OrphanStaging)
	}
}

func TestPrintPendingVerboseIncludesDebugState(t *testing.T) {
	tmp := t.TempDir()
	localPath := filepath.Join(tmp, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printPendingVerbose(&out, []vfs.PendingFile{{
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
		if len(fs.Pending()) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}

func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.Uploads)
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
