package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSViewRuntimeRebasesCachedPaths(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSViewRuntime(fs)
	fs.view.mu.Lock()
	fs.view.entries["/old/file.txt"] = drive.Entry{ID: "file", Name: "file.txt"}
	fs.view.entries["/other.txt"] = drive.Entry{ID: "other", Name: "other.txt"}
	runtime.RebaseCachedPathsLocked("/old", "/new")
	_, oldOK := fs.view.entries["/old/file.txt"]
	_, newOK := fs.view.entries["/new/file.txt"]
	_, otherOK := fs.view.entries["/other.txt"]
	fs.view.mu.Unlock()
	if oldOK || !newOK || !otherOK {
		t.Fatalf("old=%v new=%v other=%v", oldOK, newOK, otherOK)
	}
}

func TestVFSViewRuntimeTracksRecentLocalDirs(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSViewRuntime(fs)
	fs.view.mu.Lock()
	runtime.MarkLocalDirLocked("/fresh")
	fs.view.localDirs["/expired"] = time.Now().Add(-time.Minute)
	fs.view.mu.Unlock()

	if !runtime.IsRecentLocalDir("/fresh") {
		t.Fatal("fresh local dir should be recent")
	}
	if runtime.IsRecentLocalDir("/expired") {
		t.Fatal("expired local dir should not be recent")
	}
}

func TestVFSViewRuntimeMovesAndClearsLocalModTime(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSViewRuntime(fs)
	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	runtime.SetLocalModTime("/old/file.txt", modTime)

	entry := runtime.ApplyLocalModTime("/old/file.txt", drive.Entry{Name: "file.txt"})
	if !entry.ModTime.Equal(modTime) || !entry.UpdatedAt.Equal(modTime) {
		t.Fatalf("entry mod times = %+v", entry)
	}
	runtime.MoveLocalModTime("/old", "/new")
	if got := runtime.LocalModTimeFor("/old/file.txt"); !got.IsZero() {
		t.Fatalf("old mod time = %v", got)
	}
	if got := runtime.LocalModTimeFor("/new/file.txt"); !got.Equal(modTime) {
		t.Fatalf("new mod time = %v, want %v", got, modTime)
	}
	runtime.ClearLocalModTime("/new")
	if got := runtime.LocalModTimeFor("/new/file.txt"); !got.IsZero() {
		t.Fatalf("cleared mod time = %v", got)
	}
}

func TestVFSViewRuntimeRefreshesListCache(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSViewRuntime(fs)
	fs.view.mu.Lock()
	fs.view.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	fs.view.mu.Unlock()

	runtime.RefreshPath("/dir")
	fs.view.mu.RLock()
	_, ok := fs.view.lists["/dir"]
	fs.view.mu.RUnlock()
	if ok {
		t.Fatal("list cache should be refreshed")
	}
}

func TestVFSViewRuntimeCommitsEntryLocalModTime(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSViewRuntime(fs)
	modTime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	fs.view.mu.Lock()
	fs.view.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	fs.view.mu.Unlock()

	runtime.CommitEntryLocalModTime("/dir/file.txt", drive.Entry{ID: "file", Name: "file.txt"}, modTime)
	fs.view.mu.RLock()
	entry, entryOK := fs.view.entries["/dir/file.txt"]
	_, listOK := fs.view.lists["/dir"]
	fs.view.mu.RUnlock()
	if !entryOK || !entry.ModTime.Equal(modTime) {
		t.Fatalf("entry=%+v ok=%v", entry, entryOK)
	}
	if listOK {
		t.Fatal("parent list cache should be invalidated")
	}
	if got := runtime.LocalModTimeFor("/dir/file.txt"); !got.Equal(modTime) {
		t.Fatalf("local mod time = %v, want %v", got, modTime)
	}
}
