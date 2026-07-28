package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSVisibilityRuntimeDeletesAndRestoresPath(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSVisibilityRuntime(fs)
	entry := drive.Entry{ID: "dir", Name: "dir", IsDir: true}
	fs.view.mu.Lock()
	fs.view.entries["/dir"] = entry
	fs.view.entries["/dir/file.txt"] = drive.Entry{ID: "file", Name: "file.txt"}
	fs.view.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	fs.view.mu.Unlock()

	runtime.MarkDeleted("/dir", entry)
	if !runtime.IsDeleted("/dir/file.txt") {
		t.Fatal("child should be deleted through deleted directory")
	}
	fs.view.mu.RLock()
	_, childCached := fs.view.entries["/dir/file.txt"]
	_, listCached := fs.view.lists["/dir"]
	fs.view.mu.RUnlock()
	if childCached || listCached {
		t.Fatalf("deleted directory cache child=%v list=%v", childCached, listCached)
	}

	restored, ok := runtime.RestoreDeletedPath("/dir")
	if !ok || restored.ID != "dir" {
		t.Fatalf("restore = %+v ok=%v", restored, ok)
	}
	if runtime.IsDeleted("/dir") || !runtime.IsUnderRestoredDir("/dir/file.txt") {
		t.Fatal("restored directory should be visible and mark descendants restored")
	}
}

func TestVFSVisibilityRuntimeUpdatesRenameOverlay(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSVisibilityRuntime(fs)
	runtime.AddRenameOverlay("/old.txt", "/new.txt", "file", false)
	if !runtime.IsHidden("/old.txt") {
		t.Fatal("old path should be hidden while rename overlay is active")
	}

	runtime.UpdateRenameOverlay("/", []drive.Entry{{ID: "file", Name: "new.txt"}})
	if runtime.IsHidden("/old.txt") {
		t.Fatal("overlay should be removed after old path is gone and new path is visible")
	}
}

func TestVFSVisibilityRuntimeCopyHiddenExpiresAndUnhides(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSVisibilityRuntime(fs)
	runtime.SetCopyHidden("/dir", map[string]time.Time{
		"hidden.txt":  time.Now().Add(time.Minute),
		"expired.txt": time.Now().Add(-time.Minute),
	})

	if !runtime.IsCopyHidden("/dir/hidden.txt") {
		t.Fatal("expected hidden copy child")
	}
	if runtime.IsCopyHidden("/dir/expired.txt") {
		t.Fatal("expired copy child should be visible")
	}
	runtime.UnhideCopyChild("/dir", "hidden.txt")
	if runtime.IsCopyHidden("/dir/hidden.txt") {
		t.Fatal("unhidden copy child should be visible")
	}
}
