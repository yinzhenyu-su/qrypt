package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
		vfs.PendingUpload
	}{
		Op: "dirty",
		PendingUpload: vfs.PendingUpload{
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
		vfs.PendingUpload
	}{
		Op:            "clean",
		PendingUpload: vfs.PendingUpload{Path: "/old.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(dirty) + "\n" + string(clean) + "\n" + "{bad json\n"
	if err := os.WriteFile(journalPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	report := InspectCache(DebugCacheTarget{Name: "test", Dir: cacheDir})
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
