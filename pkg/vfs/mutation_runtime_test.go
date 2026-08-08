package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

type fakeMutationBackend struct {
	entries     []drive.Entry
	mkdirErr    error
	mkdirResult drive.Entry
}

func (b *fakeMutationBackend) List(context.Context, string) ([]drive.Entry, error) {
	return append([]drive.Entry(nil), b.entries...), nil
}

func (b *fakeMutationBackend) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return b.mkdirResult, b.mkdirErr
}

func (b *fakeMutationBackend) Rename(context.Context, drive.Entry, string) error {
	return nil
}

func (b *fakeMutationBackend) Move(context.Context, drive.Entry, string) error {
	return nil
}

// recordingViewCommitter records ViewCommitter calls (commit + cache) so
// coordinator tests can assert exactly when and how commits happen.
type recordingViewCommitter struct {
	committed []string
	removed   []string
	cached    int
}

func (r *recordingViewCommitter) CommitMkdir(path string, _ drive.Entry) {
	r.committed = append(r.committed, path)
}

func (r *recordingViewCommitter) CommitRemove(path string, _ drive.Entry) {
	r.removed = append(r.removed, path)
}

func (r *recordingViewCommitter) CacheListedChildren(string, []drive.Entry) {
	r.cached++
}

func (r *recordingViewCommitter) CommitUploadedEntry(string, drive.Entry, string) {}

// recordingMutationRuntime implements the remaining Rename-time mutation
// runtime surface.
type recordingMutationRuntime struct{}

func (r *recordingMutationRuntime) CommitRemoteRename(string, string, drive.Entry) drive.Entry {
	return drive.Entry{}
}

func (r *recordingMutationRuntime) InvalidateReadCache(drive.Entry) {}

func (r *recordingMutationRuntime) RenamePendingUpload(string, string, PendingUpload) error {
	return nil
}

func TestFindExistingChildDirUsesMutationBackendAndCachesChildren(t *testing.T) {
	backend := &fakeMutationBackend{entries: []drive.Entry{
		{ID: "file", ParentID: "root", Name: "file.txt"},
		{ID: "dir", ParentID: "root", Name: "dir", IsDir: true},
	}}
	committer := &recordingViewCommitter{}
	entry, err := findExistingChildDir(context.Background(), backend, committer, "/", "root", "dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "dir" || !entry.IsDir {
		t.Fatalf("entry = %+v, want existing dir", entry)
	}
	if committer.cached != 1 {
		t.Fatalf("CacheListedChildren calls = %d, want 1", committer.cached)
	}
}

func TestVFSMutationRuntimeCommitsMkdirAndRename(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSMutationRuntime(fs)
	dir := drive.Entry{ID: "dir", ParentID: fs.rootID, Name: "dir", IsDir: true}
	runtime.CommitMkdir("/dir", dir)
	if entry, err := fs.Stat(context.Background(), "/dir"); err != nil || entry.ID != "dir" || !entry.IsDir {
		t.Fatalf("stat mkdir entry=%+v err=%v", entry, err)
	}

	modTime := time.Unix(1234, 0)
	fs.setLocalModTime("/dir", modTime)
	renamed := runtime.CommitRemoteRename("/dir", "/renamed", drive.Entry{ID: "dir", ParentID: fs.rootID, Name: "renamed", IsDir: true})
	if renamed.Name != "renamed" || !renamed.ModTime.Equal(modTime) {
		t.Fatalf("renamed entry = %+v, want local modtime preserved", renamed)
	}
	if _, err := fs.Stat(context.Background(), "/dir"); err == nil {
		t.Fatal("old path should not remain stat-able")
	}
	if entry, err := fs.Stat(context.Background(), "/renamed"); err != nil || entry.ID != "dir" {
		t.Fatalf("stat renamed entry=%+v err=%v", entry, err)
	}
}
