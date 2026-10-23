package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

func TestNestedDirectoryRenameUploadsToNestedParent(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := New(localfs.New(remote), Options{RootID: remote, StorageDir: t.TempDir(), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(ctx) })
	for _, dir := range []string{"/dir", "/dir/sub"} {
		if _, err := fs.Mkdir(ctx, dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fs.WriteAt(ctx, "/dir/sub/a.txt", []byte("nested"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/dir/sub/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/dir", "/moved"); err != nil {
		t.Fatal(err)
	}
	pending, ok := fs.uploads.Store().UploadByPath("/moved/sub/a.txt")
	if !ok {
		t.Fatal("pending disappeared")
	}
	if err := fs.uploads.engine.Execute(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(remote, "moved", "sub", "a.txt")); err != nil || string(got) != "nested" {
		t.Fatalf("nested upload: data=%q err=%v", got, err)
	}
}

func TestPendingRenamePreservesOccupiedDestination(t *testing.T) {
	ctx := context.Background()
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(ctx) })
	for _, path := range []string{"/source", "/target"} {
		if _, err := fs.WriteAt(ctx, path, []byte(path), 0); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := fs.uploads.Store().UploadByPath("/target")
	if err := fs.Rename(ctx, "/source", "/target"); err == nil {
		t.Fatal("rename overwrote a pending destination")
	}
	after, ok := fs.uploads.Store().UploadByPath("/target")
	if !ok || before.FID != after.FID {
		t.Fatalf("target pending replaced: before=%q after=%q", before.FID, after.FID)
	}
	if _, ok := fs.uploads.Store().UploadByPath("/source"); !ok {
		t.Fatal("source pending lost")
	}
}

type failingParentListDriver struct {
	*localfs.Driver
	err error
}

func (d *failingParentListDriver) List(ctx context.Context, id string) ([]drive.Entry, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.Driver.List(ctx, id)
}

func TestPendingRemoveRetainsContentOnParentListError(t *testing.T) {
	ctx := context.Background()
	remote, storage := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "a"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := New(localfs.New(remote), Options{RootID: remote, StorageDir: storage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/dir/a", []byte("edited"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	drv := &failingParentListDriver{Driver: localfs.New(remote), err: errors.New("parent listing failed")}
	fs, err := New(drv, Options{RootID: remote, StorageDir: storage})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(ctx) })
	if err := fs.Remove(ctx, "/dir/a"); !errors.Is(err, drv.err) {
		t.Fatalf("remove error = %v, want %v", err, drv.err)
	}
	if _, ok := fs.uploads.Store().UploadByPath("/dir/a"); !ok {
		t.Fatal("pending content lost")
	}
}

type blockingRemoveCommitter struct {
	view.Committer
	reached, release chan struct{}
}

func (c blockingRemoveCommitter) CommitRemove(path string, entry drive.Entry) {
	close(c.reached)
	<-c.release
	c.Committer.CommitRemove(path, entry)
}

func TestDirectoryRemovalSerializesChildWrite(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := New(localfs.New(remote), Options{RootID: remote, StorageDir: t.TempDir(), DeleteDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(ctx) })
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	commit := blockingRemoveCommitter{Committer: newVFSViewCommitter(fs), reached: make(chan struct{}), release: make(chan struct{})}
	removeDone := make(chan error, 1)
	go func() { removeDone <- fs.removeDirWithRuntime(ctx, "/dir", commit) }()
	select {
	case <-commit.reached:
	case <-time.After(5 * time.Second):
		close(commit.release)
		t.Fatal("remove did not reach commit")
	}
	writeDone := make(chan error, 1)
	go func() { _, err := fs.WriteAt(ctx, "/dir/new", []byte("new"), 0); writeDone <- err }()
	select {
	case err := <-writeDone:
		close(commit.release)
		t.Fatalf("child write completed inside directory removal transaction: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(commit.release)
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	<-writeDone
}
