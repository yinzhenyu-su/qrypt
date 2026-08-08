package vfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVFSStagesUploadsAndReadsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}

	fs, err := vfs.New(raw, vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
	if history.ParentRemoteID == "" || history.ResultRemoteID == "" || len(history.Hashes) != 1 || history.Hashes[0] != string(drive.HashSHA256) {
		t.Fatalf("upload history missing transfer metadata: %+v", history)
	}
	if history.Mount != "default" || history.Driver != "localfs" {
		t.Fatalf("upload history missing mount metadata: %+v", history)
	}
	var read drive.MetricEvent
	for _, event := range snapshot.Mounts[0].ReadEvents() {
		if event.Phase == "read" {
			read = event
			break
		}
	}
	if read.Phase != "read" {
		t.Fatalf("read history missing summary event: %+v", snapshot.Mounts[0].ReadEvents())
	}
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &fileUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &sourceOnlyUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
func TestVFSKeepsLocalModTimeAfterUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "index.html"), []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
func TestVFSCoalescesSpacedFlushUploads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := newBlockingUploadDriver()
	fs, err := vfs.New(drv, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   testUploadDelay,
		UploadWorkers: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
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
