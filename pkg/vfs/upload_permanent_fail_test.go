package vfs_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newPermanentFailFS(t *testing.T) (*vfs.VFS, *drive.FakeDriver) {
	t.Helper()
	d := drive.NewFakeDriver()
	d.ErrPutSource = fmt.Errorf("%w: permanent", drive.ErrAuth)
	fs, err := vfs.New(d, vfs.Options{StorageDir: t.TempDir(), UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fs.Start(ctx)
	t.Cleanup(func() { stopVFS(t, fs) })

	if _, err := fs.Mkdir(ctx, "/d"); err != nil {
		t.Fatal(err)
	}
	writeAndFlush(t, fs, "/d/f.txt", "content")
	waitForPermanentFail(t, fs, "/d/f.txt")
	return fs, d
}

func waitForPermanentFail(t *testing.T, fs *vfs.VFS, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, pending := range fs.PendingUploads() {
			if pending.Path == path && pending.PermanentFail {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("upload %q never became permanent: %+v", path, fs.PendingUploads())
}

// TestRemoveClearsPermanentlyFailedPending pins that rm cascades to a
// terminal pending record when the remote lookup succeeds; otherwise the
// record would block idle forever with no user-visible way to clear it.
func TestRemoveClearsPermanentlyFailedPending(t *testing.T) {
	fs, _ := newPermanentFailFS(t)
	if err := fs.Remove(context.Background(), "/d/f.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending after remove = %+v, want none", pending)
	}
}

// TestRemovePermanentFailureReportsRecovery pins the terminal error path:
// when the same backend failure also blocks the remote lookup, rm cannot
// safely decide whether an older remote object exists and must point at the
// offline cleanup command instead of silently dropping the record.
func TestRemovePermanentFailureReportsRecovery(t *testing.T) {
	fs, d := newPermanentFailFS(t)
	d.ErrList = fmt.Errorf("%w: auth expired", drive.ErrAuth)
	err := fs.Remove(context.Background(), "/d/f.txt")
	if err == nil {
		t.Fatal("Remove error = nil, want reconciliation failure")
	}
	if !strings.Contains(err.Error(), "journal prune --failed") {
		t.Fatalf("Remove error lacks recovery guidance: %v", err)
	}
	if pending := fs.PendingUploads(); len(pending) != 1 {
		t.Fatalf("pending after failed remove = %+v, want the terminal record kept", pending)
	}
}
