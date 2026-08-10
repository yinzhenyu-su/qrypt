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
	renameErr   error
	renameCalls int
	moveErr     error
	moveCalls   int
	moveResult  error
}

func (b *fakeMutationBackend) List(context.Context, string) ([]drive.Entry, error) {
	return append([]drive.Entry(nil), b.entries...), nil
}

func (b *fakeMutationBackend) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return b.mkdirResult, b.mkdirErr
}

func (b *fakeMutationBackend) Rename(context.Context, drive.Entry, string) error {
	b.renameCalls++
	return b.renameErr
}

func (b *fakeMutationBackend) Move(context.Context, drive.Entry, string) error {
	b.moveCalls++
	return b.moveErr
}

// recordingViewCommitter records ViewCommitter calls (commit + cache) so
// coordinator tests can assert exactly when and how commits happen.
type recordingViewCommitter struct {
	committed []string
	removed   []string
	renamed   [][2]string
	// renamedEntry holds the entry passed to the most recent
	// CommitRemoteRename, so tests can assert the committed metadata.
	renamedEntry drive.Entry
	cached       int
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

func (r *recordingViewCommitter) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	r.renamed = append(r.renamed, [2]string{oldPath, newPath})
	r.renamedEntry = entry
}

// recordingMutationRuntime implements the remaining Rename-time mutation
// runtime surface.
type recordingMutationRuntime struct{}

func (r *recordingMutationRuntime) InvalidateReadCache(drive.Entry) {}

func (r *recordingMutationRuntime) RenamePendingUpload(string, string, PendingUpload) error {
	return nil
}

func TestVFSMutationRuntimeCommitsMkdirAndRename(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	committer := newVFSViewCommitter(fs)
	dir := drive.Entry{ID: "dir", ParentID: fs.rootID, Name: "dir", IsDir: true}
	committer.CommitMkdir("/dir", dir)
	if entry, err := fs.Stat(context.Background(), "/dir"); err != nil || entry.ID != "dir" || !entry.IsDir {
		t.Fatalf("stat mkdir entry=%+v err=%v", entry, err)
	}

	modTime := time.Unix(1234, 0)
	fs.setLocalModTime("/dir", modTime)
	newVFSViewCommitter(fs).CommitRemoteRename("/dir", "/renamed", drive.Entry{ID: "dir", ParentID: fs.rootID, Name: "renamed", IsDir: true})
	if _, err := fs.Stat(context.Background(), "/dir"); err == nil {
		t.Fatal("old path should not remain stat-able")
	}
	if entry, err := fs.Stat(context.Background(), "/renamed"); err != nil || entry.ID != "dir" {
		t.Fatalf("stat renamed entry=%+v err=%v", entry, err)
	}
	if entry, err := fs.Stat(context.Background(), "/renamed"); err != nil || !entry.ModTime.Equal(modTime) {
		t.Fatalf("renamed entry modtime = %v err=%v, want local modtime preserved", entry.ModTime, err)
	}
}
