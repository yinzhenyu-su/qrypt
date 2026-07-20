package vfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSStagesUploadsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}

	fs, err := vfs.New(raw, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/hello.txt", []byte("hello qrypt"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/hello.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rc, err := fs.Read(ctx, "/hello.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello qrypt" {
		t.Fatalf("unexpected data: %q", data)
	}
	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 || len(snapshot.Mounts[0].HistoricalUploads()) != 1 {
		t.Fatalf("expected one upload history item, got %+v", snapshot)
	}
	history := snapshot.Mounts[0].HistoricalUploads()[0]
	if history.Path != "/hello.txt" || history.State != string(drive.UploadPhaseCompleted) || history.BytesUploaded != int64(len("hello qrypt")) {
		t.Fatalf("unexpected upload history: %+v", history)
	}
	if history.StageDurations[string(drive.UploadPhaseUploading)] == "" {
		t.Fatalf("upload history missing upload stage duration: %+v", history)
	}
	if history.ParentRemoteID == "" || history.ResultRemoteID == "" || len(history.Hashes) != 3 {
		t.Fatalf("upload history missing transfer metadata: %+v", history)
	}
	if history.Mount != "default" || history.Driver != "localfs" {
		t.Fatalf("upload history missing mount metadata: %+v", history)
	}
	if len(snapshot.Mounts[0].ReadEvents()) != 1 {
		t.Fatalf("read history count = %d, want 1: %+v", len(snapshot.Mounts[0].ReadEvents()), snapshot.Mounts[0].ReadEvents())
	}
	read := snapshot.Mounts[0].ReadEvents()[0]
	if read.Kind != "vfs_read" || read.Operation != "read" || !read.OK || read.Path != "/hello.txt" || read.RemoteID == "" || read.Bytes != int64(len("hello qrypt")) || read.State != "completed" {
		t.Fatalf("unexpected read history: %+v", read)
	}
	report, err := fs.DebugConsistency(ctx, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || !report.RemoteFound || !report.SizeMatches {
		t.Fatalf("unexpected consistency report: %+v", report)
	}
}

func TestVFSUsesSourceUploaderForStagingSnapshot(t *testing.T) {
	ctx := context.Background()
	drv := &fileUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("use staging path"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if drv.putSourceCalls != 1 || drv.putCalls != 0 {
		t.Fatalf("putSourceCalls=%d putCalls=%d, want 1 and 0", drv.putSourceCalls, drv.putCalls)
	}
	if drv.sourceOpens != 1 {
		t.Fatalf("sourceOpens=%d, want 1", drv.sourceOpens)
	}
	if string(drv.lastData) != "use staging path" {
		t.Fatalf("unexpected uploaded data: %q", drv.lastData)
	}
	if !drv.lastHasSHA256 {
		t.Fatal("source did not provide SHA-256 metadata")
	}
	want := sha256.Sum256([]byte("use staging path"))
	if !bytes.Equal(drv.lastSHA256, want[:]) {
		t.Fatalf("source SHA-256 = %x, want %x", drv.lastSHA256, want)
	}
}

func TestVFSUploadsWithSourceOnlyDriver(t *testing.T) {
	ctx := context.Background()
	drv := &sourceOnlyUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/source-only.txt", []byte("source only"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/source-only.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if drv.calls != 1 {
		t.Fatalf("source-only calls = %d, want 1", drv.calls)
	}
	if string(drv.lastData) != "source only" {
		t.Fatalf("unexpected uploaded data: %q", drv.lastData)
	}
}

func TestVFSDebugUploadCancelRequeuesAndRetries(t *testing.T) {
	ctx := context.Background()
	drv := &cancelAwareUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	content := []byte(strings.Repeat("resume-test", 128))
	if _, err := fs.WriteAt(ctx, "/resume-debug.bin", content, 0); err != nil {
		t.Fatal(err)
	}
	result, err := fs.DebugInjectUploadCancel(ctx, vfs.DebugUploadCancelRequest{
		Path:       "/resume-debug.bin",
		Phase:      drive.UploadPhaseUploading,
		AfterBytes: 1,
		Once:       true,
		Reason:     "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Armed || result.ID == "" {
		t.Fatalf("unexpected fault result: %+v", result)
	}
	if err := fs.Flush(ctx, "/resume-debug.bin"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	attempts, canceled := drv.state()
	if attempts < 2 {
		t.Fatalf("upload attempts = %d, want retry after debug cancel", attempts)
	}
	if !canceled {
		t.Fatal("driver did not observe context cancellation")
	}
	if faults := fs.DebugUploadCancelFaults(ctx); len(faults) != 0 {
		t.Fatalf("one-shot debug fault was not cleared: %+v", faults)
	}
	entry, err := fs.Stat(ctx, "/resume-debug.bin")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("uploaded size = %d, want %d", entry.Size, len(content))
	}
}

func TestVFSUploadUsesStableSnapshotWhenFileChangesDuringUpload(t *testing.T) {
	ctx := context.Background()
	drv := &fileUploadDriver{blockFirst: make(chan struct{}), firstEntered: make(chan struct{})}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("first version"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first upload did not start")
	}
	if err := fs.Truncate(ctx, "/fast.txt", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("second"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	close(drv.blockFirst)
	waitNoPending(t, fs)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if len(drv.allData) < 2 {
		t.Fatalf("uploads = %q, want superseded upload and latest upload", drv.allData)
	}
	if string(drv.allData[0]) != "first version" {
		t.Fatalf("first upload data = %q, want stable snapshot", drv.allData[0])
	}
	if string(drv.lastData) != "second" {
		t.Fatalf("last upload data = %q, want second", drv.lastData)
	}
}

func TestVFSKeepsLocalModTimeAfterUpload(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "index.html"), []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/index.html", []byte("new content"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/index.html"); err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1234, 5678)
	if err := fs.SetModTime(ctx, "/index.html", want); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	entry, err := fs.Stat(ctx, "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.ModTime.Equal(want) {
		t.Fatalf("stat modtime = %s, want %s", entry.ModTime, want)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].ModTime.Equal(want) {
		t.Fatalf("list entries = %+v, want modtime %s", entries, want)
	}
}

func TestVFSCoalescesFlushUploads(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "two" {
		t.Fatalf("last upload = %q, want two", got)
	}
}

func TestVFSReplaceUploadKeepsExistingFileUntilUploadSucceeds(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{
		entries: map[string]drive.Entry{
			"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
		},
		failUploads: 1,
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
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
		pending := fs.Pending()
		return len(pending) == 1 && pending[0].RetryCount == 1 && pending[0].LastError != ""
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
	ctx := context.Background()
	drv := &countingUploadDriver{
		entries: map[string]drive.Entry{
			"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
		},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
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
	cacheDir := t.TempDir()
	entries := map[string]drive.Entry{
		"old": {ID: "old", ParentID: "0", Name: "draft.txt", Size: 3},
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDriver := &countingUploadDriver{entries: entries, failRenames: 1}
	first, err := vfs.New(firstDriver, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
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
		pending := first.Pending()
		return len(pending) == 1 && pending[0].ReplaceUpload != nil && pending[0].RetryCount == 1
	})
	if got := firstDriver.uploadCount(); got != 1 {
		t.Fatalf("first upload count = %d, want 1", got)
	}
	if removed := firstDriver.removedIDs(); len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("first removed ids = %v, want [old]", removed)
	}
	cancelFirst()

	secondDriver := &countingUploadDriver{entries: entries}
	second, err := vfs.New(secondDriver, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(context.Background())
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

func TestVFSUploadRetryUsesGrowingBackoff(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{failUploads: 2}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/retry.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/retry.txt"); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, func() bool {
		pending := fs.Pending()
		return len(pending) == 1 && pending[0].RetryCount == 2
	})
	pending := fs.Pending()[0]
	delay := time.Unix(0, pending.NextAttemptAt).Sub(time.Unix(0, pending.LastAttemptAt))
	if delay < 700*time.Millisecond {
		t.Fatalf("retry delay = %s, want exponential backoff after second failure", delay)
	}
}

func TestVFSResumePendingWaitsUntilNextAttempt(t *testing.T) {
	cacheDir := t.TempDir()
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDriver := &countingUploadDriver{failUploads: 1}
	first, err := vfs.New(firstDriver, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	first.Start(firstCtx)
	if _, err := first.WriteAt(firstCtx, "/resume-retry.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(firstCtx, "/resume-retry.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		pending := first.Pending()
		return len(pending) == 1 && pending[0].RetryCount == 1 && pending[0].NextAttemptAt > time.Now().Add(200*time.Millisecond).UnixNano()
	})
	cancelFirst()

	secondDriver := &countingUploadDriver{}
	second, err := vfs.New(secondDriver, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(context.Background())
	time.Sleep(100 * time.Millisecond)
	if got := secondDriver.uploadCount(); got != 0 {
		t.Fatalf("resume uploaded before next attempt: count=%d", got)
	}
	waitNoPending(t, second)
	if got := secondDriver.uploadCount(); got != 1 {
		t.Fatalf("resume upload count = %d, want 1", got)
	}
}

func TestVFSZeroByteFlushWaitsForFollowUpWrite(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if got := drv.uploadCount(); got != 0 {
		t.Fatalf("zero-byte flush uploaded too early: count=%d", got)
	}

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("final"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "final" {
		t.Fatalf("last upload = %q, want final", got)
	}
}

func TestVFSCoalescesSpacedFlushUploads(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	for _, data := range []string{"one", "two", "three"} {
		if _, err := fs.WriteAt(ctx, "/log.txt", []byte(data), 0); err != nil {
			t.Fatal(err)
		}
		if err := fs.Flush(ctx, "/log.txt"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "three" {
		t.Fatalf("last upload = %q, want three", got)
	}
}

func TestVFSUploadWorkersRunConcurrently(t *testing.T) {
	ctx := context.Background()
	drv := newBlockingUploadDriver()
	fs, err := vfs.New(drv, vfs.Options{
		CacheDir:      t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   testUploadDelay,
		UploadWorkers: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	for _, path := range []string{"/one.txt", "/two.txt", "/three.txt"} {
		if _, err := fs.WriteAt(ctx, path, []byte(path), 0); err != nil {
			t.Fatal(err)
		}
		if err := fs.Flush(ctx, path); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 3; i++ {
		select {
		case <-drv.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("upload worker %d did not start", i+1)
		}
	}
	close(drv.release)
	waitNoPending(t, fs)
}

func TestVFSUploadDoesNotClearNewerPending(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &countingUploadDriver{entered: entered, blockReturn: release}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	close(release)

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 2 {
		t.Fatalf("upload count = %d, want 2", got)
	}
	if got := drv.lastUpload(); got != "two" {
		t.Fatalf("last upload = %q, want two", got)
	}
	if removed := drv.removedIDs(); len(removed) != 1 || removed[0] != "draft.txt-1" {
		t.Fatalf("removed stale uploads = %v, want [draft.txt-1]", removed)
	}
}

func TestVFSAppleMetadataWrittenAndUploaded(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if n, err := fs.WriteAt(ctx, "/.DS_Store", []byte("finder"), 0); err != nil || n != len("finder") {
		t.Fatalf("WriteAt .DS_Store n=%d err=%v", n, err)
	}
	if err := fs.Flush(ctx, "/.DS_Store"); err != nil {
		t.Fatal(err)
	}

	// After flush, the file is pending and will be uploaded (like any normal file).
	pending := fs.Pending()
	if len(pending) != 1 || pending[0].Name != ".DS_Store" {
		t.Fatalf("pending = %v, want [.DS_Store]", pending)
	}

	// Stat finds the pending file.
	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 6 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}

func TestVFSRemoteAppleMetadataVisible(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{entries: map[string]drive.Entry{
		"meta":   {ID: "meta", ParentID: "0", Name: ".DS_Store", Size: 1},
		"double": {ID: "double", ParentID: "0", Name: "._asset.js", Size: 1},
		"file":   {ID: "file", ParentID: "0", Name: "asset.js", Size: 1},
	}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	// Apple metadata files are now visible like any other file.
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	got := namesOf(entries)
	if !strings.Contains(got, ".DS_Store") || !strings.Contains(got, "._asset.js") || !strings.Contains(got, "asset.js") {
		t.Fatalf("entries = %q, want all three entries including .DS_Store and ._asset.js", got)
	}

	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 1 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}

func TestVFSRecoversPendingUploads(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/resume.txt", []byte("resume me"), 0); err != nil {
		t.Fatal(err)
	}
	if len(first.Pending()) != 1 {
		t.Fatalf("expected one pending file, got %d", len(first.Pending()))
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(ctx)
	waitNoPending(t, second)

	data, err := os.ReadFile(remote + "/resume.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resume me" {
		t.Fatalf("unexpected recovered data: %q", data)
	}
}

func TestVFSRecoversUnflushedPendingUploadSizeFromStaging(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	content := bytes.Repeat([]byte("x"), 2*1024*1024+123)

	first, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for off := 0; off < len(content); off += 16 * 1024 {
		end := off + 16*1024
		if end > len(content) {
			end = len(content)
		}
		if _, err := first.WriteAt(ctx, "/resume-large.bin", content[off:end], int64(off)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(first.Pending()); got != 1 {
		t.Fatalf("expected one pending file, got %d", got)
	}
	journal, err := os.ReadFile(filepath.Join(cache, "pending.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(journal), "\n"); lines != 1 {
		t.Fatalf("pending journal lines after unflushed writes = %d, want 1", lines)
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	pending := second.Pending()
	if len(pending) != 1 {
		t.Fatalf("expected one recovered pending file, got %d", len(pending))
	}
	if pending[0].Size != 0 {
		t.Fatalf("pending size before resume repair = %d, want stale journal size 0", pending[0].Size)
	}

	second.Start(ctx)
	waitNoPending(t, second)
	data, err := os.ReadFile(filepath.Join(remote, "resume-large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("recovered data mismatch: got %d bytes, want %d", len(data), len(content))
	}
}

func TestVFSDropsPendingWhenStagingMissingOnRecovery(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/lost.txt", []byte("lost data"), 0); err != nil {
		t.Fatal(err)
	}
	pending := first.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if err := os.Remove(pending[0].LocalPath); err != nil {
		t.Fatal(err)
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if pending := second.Pending(); len(pending) != 0 {
		t.Fatalf("pending with missing staging should not recover: %+v", pending)
	}
}

func TestVFSWriteAtStagesExistingFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "data.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{RootID: remote, CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("XY"), 2); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	data, err := os.ReadFile(filepath.Join(remote, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abXYef" {
		t.Fatalf("unexpected patched backend data: %q", data)
	}
}

func TestVFSTruncateUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("abcdef"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Truncate(ctx, "/data.txt", 3); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Read(ctx, "/data.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected staged truncate data: %q", data)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err = os.ReadFile(remote + "/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected truncated backend data: %q", data)
	}
}
