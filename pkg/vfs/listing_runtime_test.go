package vfs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

type fakeListingRuntime struct {
	currentPrefetch bool
	committedPath   string
	committed       []drive.Entry
}

func (r *fakeListingRuntime) FreshCachedList(string, time.Time) ([]drive.Entry, bool) {
	return nil, false
}

func (r *fakeListingRuntime) CommitRemoteList(parentPath string, entries []drive.Entry, _ time.Time) []drive.Entry {
	r.committedPath = parentPath
	r.committed = cloneEntries(entries)
	return entries
}

func (r *fakeListingRuntime) PendingChildren(string, []drive.Entry) []drive.Entry {
	return nil
}

func (r *fakeListingRuntime) IsCurrentPrefetchDir(string, string) bool {
	return r.currentPrefetch
}

type fakeListBackend struct {
	entries []drive.Entry
	err     error
}

func (b fakeListBackend) ListChildren(context.Context, string) ([]drive.Entry, error) {
	return b.entries, b.err
}

func TestLoadRemoteChildrenWithRuntimeCommitsBackendEntries(t *testing.T) {
	runtime := &fakeListingRuntime{currentPrefetch: true}
	entries, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", false, runtime, fakeListBackend{
		entries: []drive.Entry{{ID: "child", Name: "child.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "child" {
		t.Fatalf("entries = %+v", entries)
	}
	if runtime.committedPath != "/dir" || len(runtime.committed) != 1 {
		t.Fatalf("commit path=%q entries=%+v", runtime.committedPath, runtime.committed)
	}
}

func TestLoadRemoteChildrenWithRuntimeDiscardsStalePrefetch(t *testing.T) {
	runtime := &fakeListingRuntime{currentPrefetch: false}
	_, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", true, runtime, fakeListBackend{
		entries: []drive.Entry{{ID: "child", Name: "child.txt"}},
	})
	if err == nil {
		t.Fatal("expected stale prefetch error")
	}
	if len(runtime.committed) != 0 {
		t.Fatalf("stale prefetch committed entries: %+v", runtime.committed)
	}
}

func TestLoadRemoteChildrenWithRuntimeReturnsBackendError(t *testing.T) {
	wantErr := errors.New("list failed")
	_, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", false, &fakeListingRuntime{}, fakeListBackend{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestVFSListingRuntimeAddsVisiblePendingChildren(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSListingRuntime(fs)
	base := []drive.Entry{{ID: "existing", Name: "existing.txt"}}
	if err := fs.uploads.store.SaveUpload(PendingUpload{
		Path:     "/dir/pending.txt",
		FID:      "pending",
		ParentID: "dir",
		Name:     "pending.txt",
		Size:     7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.uploads.store.SaveUpload(PendingUpload{
		Path: "/other/skip.txt",
		FID:  "other",
		Name: "skip.txt",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.uploads.store.SaveUpload(PendingUpload{
		Path: "/dir/existing.txt",
		FID:  "duplicate",
		Name: "existing.txt",
	}); err != nil {
		t.Fatal(err)
	}
	newVFSVisibilityRuntime(fs).MarkDeleted("/dir/deleted.txt", drive.Entry{ID: "deleted", Name: "deleted.txt"})
	if err := fs.uploads.store.SaveUpload(PendingUpload{
		Path: "/dir/deleted.txt",
		FID:  "deleted",
		Name: "deleted.txt",
	}); err != nil {
		t.Fatal(err)
	}

	entries := runtime.PendingChildren("/dir", base)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[1].ID != "pending" || entries[1].Name != "pending.txt" || entries[1].Size != 7 {
		t.Fatalf("pending entry = %+v", entries[1])
	}
}
