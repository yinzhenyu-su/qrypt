package vfs_test

import (
	"context"
	"os"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

// TestVerifyCrashBetweenUploadAndRecordDiscardsBytes models a process death in
// the window between "the temporary upload finished" and "the replacement is
// recorded". The remote has the bytes, the journal does not mention them, so the
// next mount classifies the temporary entry as stale, deletes it, and uploads
// the file again from scratch. The window is exactly one journal append wide.
func TestVerifyCrashBetweenUploadAndRecordDiscardsBytes(t *testing.T) {
	cacheDir := t.TempDir()
	contents := []byte("new!")

	// The journal as the process left it: a frozen generation, no replacement
	// recorded yet.
	store, err := upload.NewPendingStore(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	fid := upload.NewStagingFID("/draft.txt")
	local, err := store.CreateStaging(fid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	pending := upload.PendingUpload{
		Path:      "/draft.txt",
		FID:       fid,
		ParentID:  "0",
		Name:      "draft.txt",
		LocalPath: local,
		Size:      int64(len(contents)),
		Frozen:    true,
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}

	// The remote as the process left it: the temporary upload completed, so the
	// bytes are there under the temporary name and the old file is still there.
	tempName := upload.TemporaryUploadName("draft.txt", fid)
	tempID := tempName + "-1"
	entries := map[string]drive.Entry{
		tempID: {ID: tempID, ParentID: "0", Name: tempName, Size: int64(len(contents))},
		"old":  {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
	}
	drv := &countingUploadDriver{entries: entries}

	fs, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	waitNoPending(t, fs)

	removed := drv.removedIDs()
	t.Logf("recovered mount removed %v and uploaded the file %d time(s)", removed, drv.uploadCount())
	var removedTemp bool
	for _, id := range removed {
		if id == tempID {
			removedTemp = true
		}
	}
	if !removedTemp {
		t.Fatalf("removed = %v, want it to include the completed temporary upload %q", removed, tempID)
	}
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1 full re-upload of bytes the remote already had", got)
	}
}
