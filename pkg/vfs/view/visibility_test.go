package view

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestVisibilityDeletesAndRestoresPath(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
	entry := drive.Entry{ID: "dir", Name: "dir", IsDir: true}
	v.mu.Lock()
	v.entries.Set("/dir", entry)
	v.entries.Set("/dir/file.txt", drive.Entry{ID: "file", Name: "file.txt"})
	v.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	v.mu.Unlock()

	runtime.MarkDeleted("/dir", entry)
	if !runtime.IsDeleted("/dir/file.txt") {
		t.Fatal("child should be deleted through deleted directory")
	}
	v.mu.RLock()
	_, childCached := v.entries.Get("/dir/file.txt")
	_, listCached := v.lists["/dir"]
	v.mu.RUnlock()
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

func TestVisibilityUpdatesRenameOverlay(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
	runtime.AddRenameOverlay("/old.txt", "/new.txt", "file", false)
	if !runtime.IsHidden("/old.txt") {
		t.Fatal("old path should be hidden while rename overlay is active")
	}

	runtime.UpdateRenameOverlay("/", []drive.Entry{{ID: "file", Name: "new.txt"}})
	if runtime.IsHidden("/old.txt") {
		t.Fatal("overlay should be removed after old path is gone and new path is visible")
	}
}

func TestVisibilityCopyHiddenExpiresAndUnhides(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
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

func TestVisibilityDeleteExecutorOps(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)

	// No overlay entry yet: BeginDelete is refused.
	if runtime.BeginDelete("/a.txt", "id-a") {
		t.Fatal("BeginDelete should refuse a path with no deleted overlay entry")
	}
	runtime.MarkDeleted("/a.txt", drive.Entry{ID: "id-a", Name: "a.txt"})
	if !runtime.BeginDelete("/a.txt", "id-a") {
		t.Fatal("BeginDelete should accept the current deleted overlay entry")
	}
	if runtime.BeginDelete("/a.txt", "stale") {
		t.Fatal("BeginDelete should refuse a stale entry ID")
	}

	runtime.MarkDeleteActive("/a.txt", drive.Entry{ID: "id-a"})
	runtime.MarkDeleteFailed("/a.txt", nil)
	runtime.MarkDeleteComplete("/a.txt", drive.Entry{ID: "id-a"})
	if runtime.IsDeleted("/a.txt") {
		t.Fatal("completed delete should leave the overlay")
	}

	runtime.MarkDeleted("/b.txt", drive.Entry{ID: "id-b", Name: "b.txt"})
	runtime.MarkDeleteActive("/b.txt", drive.Entry{ID: "id-b"})
	runtime.CancelDelete("/b.txt")
	if runtime.IsDeleted("/b.txt") {
		t.Fatal("cancelled delete should leave the overlay")
	}
}
