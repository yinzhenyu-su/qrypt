package upload

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// TestDebugStateOwnsActiveAndHistorySnapshots covers the upload snapshot
// state machine over DebugState: start, byte progress, metadata, finish into
// the bounded history, and history removal.
func TestDebugStateOwnsActiveAndHistorySnapshots(t *testing.T) {
	state := NewDebugState()

	state.Start(PendingUpload{
		FID:      "op-1",
		Path:     "/file.txt",
		Name:     "file.txt",
		Size:     10,
		ParentID: "parent",
	})
	state.UpdateBytes("/file.txt", 4)
	active := state.ActiveSnapshots()
	if got := active["/file.txt"].BytesUploaded; got != 4 {
		t.Fatalf("active bytes = %d, want 4", got)
	}

	state.SetMetadata("/file.txt", "remote-id", []string{"sha1:x"})
	state.Finish("/file.txt", "done", "")
	if active := state.ActiveSnapshots(); len(active) != 0 {
		t.Fatalf("active snapshots after finish = %d, want 0", len(active))
	}
	history := state.HistorySnapshots()
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].ResultRemoteID != "remote-id" || len(history[0].Hashes) != 1 {
		t.Fatalf("history metadata not recorded: %+v", history[0])
	}
	if !state.RemoveHistoryByID("op-1") {
		t.Fatal("expected history record to be removed")
	}
	if history := state.HistorySnapshots(); len(history) != 0 {
		t.Fatalf("history length after remove = %d, want 0", len(history))
	}
}

// TestComposeSnapshotsMergesPendingAndActive derives the queued / scheduled /
// retry_wait states from the persisted pending set and the in-flight map.
func TestComposeSnapshotsMergesPendingAndActive(t *testing.T) {
	active := map[string]UploadSnapshot{
		"/active.txt": {Path: "/active.txt", State: SnapshotStateUploading},
	}
	timers := map[string]time.Time{
		"/scheduled.txt": time.Unix(0, 0),
	}
	pending := []PendingUpload{
		{Path: "/active.txt", Size: 3},
		{FID: "q", Path: "/queued.txt", Name: "queued.txt", Size: 4, ParentID: "p"},
		{Path: "/failed.txt", Size: 5, PermanentFail: true},
		{Path: "/scheduled.txt", Size: 6, LastError: "boom", NextAttemptAt: time.Now().Add(time.Minute).UnixNano()},
	}
	got := ComposeSnapshots(pending, active, timers)
	byPath := map[string]string{}
	for _, upload := range got {
		byPath[upload.Path] = upload.State
	}
	if byPath["/active.txt"] != SnapshotStateUploading {
		t.Fatalf("active state = %q, want %q", byPath["/active.txt"], SnapshotStateUploading)
	}
	if byPath["/queued.txt"] != "queued" {
		t.Fatalf("queued state = %q, want queued", byPath["/queued.txt"])
	}
	if byPath["/failed.txt"] != "failed" {
		t.Fatalf("failed state = %q, want failed", byPath["/failed.txt"])
	}
	if byPath["/scheduled.txt"] != "retry_wait" {
		t.Fatalf("scheduled state = %q, want retry_wait", byPath["/scheduled.txt"])
	}
	if len(got) != 4 {
		t.Fatalf("composed snapshots = %d, want 4", len(got))
	}
}

func TestStagingFID(t *testing.T) {
	if got := StagingFID("/"); got != "root" {
		t.Fatalf("StagingFID(/) = %q, want root", got)
	}
	if got := StagingFID("/photos/cat.jpg"); got != "photos_cat.jpg" {
		t.Fatalf("StagingFID(/photos/cat.jpg) = %q", got)
	}
	if got := NewStagingFID("/a"); got == "" || !hasPrefix(got, "a-") {
		t.Fatalf("NewStagingFID = %q, want a-*", got)
	}
	if got := vfstypes.CleanVirtualPath("/x"); got != "/x" {
		t.Fatalf("CleanVirtualPath = %q", got)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
