package upload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepUnreferencedStagingLive(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.CreateStaging("live-fid")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUpload(PendingUpload{Path: "/live", FID: "live-fid", LocalPath: live}); err != nil {
		t.Fatal(err)
	}
	stagingDir := filepath.Join(store.dir, "staging")
	oldOrphan := filepath.Join(stagingDir, "orphan-old.staging")
	freshOrphan := filepath.Join(stagingDir, "orphan-fresh.staging")
	note := filepath.Join(stagingDir, "note.txt")
	for _, path := range []string{oldOrphan, freshOrphan, note} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * liveStagingMinAge)
	if err := os.Chtimes(oldOrphan, old, old); err != nil {
		t.Fatal(err)
	}

	if cleaned := store.SweepUnreferencedStaging(); cleaned != 1 {
		t.Fatalf("SweepUnreferencedStaging removed %d files, want 1", cleaned)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("referenced staging removed: %v", err)
	}
	if _, err := os.Stat(freshOrphan); err != nil {
		t.Fatalf("fresh unreferenced staging removed before registration window closed: %v", err)
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("non-staging file removed: %v", err)
	}
	if _, err := os.Stat(oldOrphan); !os.IsNotExist(err) {
		t.Fatalf("old orphan staging not swept, err=%v", err)
	}

	// Once the fresh orphan ages past the grace window it is swept too.
	if err := os.Chtimes(freshOrphan, old, old); err != nil {
		t.Fatal(err)
	}
	if cleaned := store.SweepUnreferencedStaging(); cleaned != 1 {
		t.Fatalf("second sweep removed %d files, want 1", cleaned)
	}
	if _, err := os.Stat(freshOrphan); !os.IsNotExist(err) {
		t.Fatalf("aged orphan staging not swept, err=%v", err)
	}
}
