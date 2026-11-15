package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"strings"
	"testing"
)

func TestVFSReplaceUploadKeepsExistingFileUntilUploadSucceeds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{
		entries: map[string]drive.Entry{
			"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
		},
		// Keep every attempt failing so the retry state stays visible: with a
		// single failure the retry both clears LastError (on the replacement
		// record) and eventually drops the pending entry, so the state below
		// would only be observable inside a scheduling-dependent window.
		failAllUploads: true,
	}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, func() bool {
		pending := fs.PendingUploads()
		return len(pending) == 1 && pending[0].RetryCount >= 1 && pending[0].LastError != ""
	})
	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("existing file removed after failed temp upload: %v", removed)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "old" || entries[0].Name != "draft.txt" {
		t.Fatalf("existing remote file was not preserved: %+v", entries)
	}
}
func TestVFSReplaceUploadRenamesTemporaryFileAfterSuccess(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{
		entries: map[string]drive.Entry{
			"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
		},
	}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if removed := drv.removedIDs(); len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("removed existing ids = %v, want [old]", removed)
	}
	renamed := drv.renamedIDs()
	if len(renamed) != 1 || !strings.HasSuffix(renamed[0], ":draft.txt") {
		t.Fatalf("renamed temp uploads = %v, want one rename to draft.txt", renamed)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "draft.txt" || entries[0].Size != 3 || entries[0].ID == "old" {
		t.Fatalf("unexpected final entries: %+v", entries)
	}
}
func TestVFSResumeReplaceUploadRenamesTemporaryFileWithoutReupload(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	entries := map[string]drive.Entry{
		"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	// Keep every rename failing on the first mount: its retry must not be able
	// to finish the replacement before the test stops it, otherwise the second
	// mount has nothing to resume and the assertion below passes vacuously. The
	// retry is due as soon as the failed attempt's journal write completes, so
	// a single failure is not enough to guarantee the record survives.
	firstDriver := &countingUploadDriver{entries: entries, failAllRenames: true}
	first, err := vfs.New(firstDriver, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, first)
	first.Start(firstCtx)

	if err := first.Create(firstCtx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(firstCtx, "/draft.txt", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(firstCtx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		pending := first.PendingUploads()
		return len(pending) == 1 && pending[0].ReplaceUpload != nil && pending[0].RetryCount >= 1
	})
	if got := firstDriver.uploadCount(); got != 1 {
		t.Fatalf("first upload count = %d, want 1", got)
	}
	if removed := firstDriver.removedIDs(); len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("first removed ids = %v, want [old]", removed)
	}
	// Stop the first mount completely before the second one opens the same
	// staging directory and the same fake remote: Close waits for in-flight
	// attempts, so no worker of the old mount can race the resumed upload.
	cancelFirst()
	stopVFS(t, first)
	if pending := first.PendingUploads(); len(pending) != 1 || pending[0].ReplaceUpload == nil {
		t.Fatalf("replacement candidate did not survive the first mount: %+v", pending)
	}

	secondDriver := &countingUploadDriver{entries: entries}
	second, err := vfs.New(secondDriver, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	defer stopVFS(t, second)
	second.Start(secondCtx)
	waitNoPending(t, second)

	if got := secondDriver.uploadCount(); got != 0 {
		t.Fatalf("resume reuploaded temp file: count=%d", got)
	}
	renamed := secondDriver.renamedIDs()
	if len(renamed) != 1 || !strings.HasSuffix(renamed[0], ":draft.txt") {
		t.Fatalf("resume renamed temp uploads = %v, want one rename to draft.txt", renamed)
	}
	entriesAfter, err := second.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != 1 || entriesAfter[0].Name != "draft.txt" || entriesAfter[0].ID == "old" {
		t.Fatalf("unexpected final entries: %+v", entriesAfter)
	}
}
