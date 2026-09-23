package view

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestVisibilityDeletesAndRestoresPath(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
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
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
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
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
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
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)

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

// TestRenameShadowConvergesByName: a backend that derives ids from the
// location reports a different id for the moved object, so the shadow must
// converge on the name alone.
func TestRenameShadowConvergesByName(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
	runtime.AddRenameOverlay("/a.txt", "/b.txt", "old-id", false)

	runtime.UpdateRenameOverlay("/", []drive.Entry{{ID: "new-id", Name: "b.txt"}})
	if runtime.IsHidden("/a.txt") {
		t.Fatal("shadow must clear once the new name is listed, even with a changed id")
	}
}

// TestRenameShadowHoldsUntilBackendConverges: while the listing still carries
// the old name and not the new one, the stale entry stays hidden.
func TestRenameShadowHoldsUntilBackendConverges(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
	runtime.AddRenameOverlay("/a.txt", "/b.txt", "old-id", false)

	runtime.UpdateRenameOverlay("/", []drive.Entry{{ID: "old-id", Name: "a.txt"}})
	if !runtime.IsHidden("/a.txt") {
		t.Fatal("shadow must hold while the backend still lists only the old name")
	}
}

// TestRenameShadowRetiredByNewObject: the shadow hides the pre-rename object,
// not the path, so a different object committed there is visible at once.
func TestRenameShadowRetiredByNewObject(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
	runtime.AddRenameOverlay("/a.txt", "/b.txt", "old-id", false)

	runtime.RetireRenameShadow("/a.txt", "old-id")
	if !runtime.IsHidden("/a.txt") {
		t.Fatal("the renamed object itself must stay hidden")
	}
	runtime.RetireRenameShadow("/a.txt", "other-id")
	if runtime.IsHidden("/a.txt") {
		t.Fatal("a different object at the old path must be visible")
	}
}

// TestRenameShadowExpires: the shadow is bounded, so a backend that never
// reports the expected shape cannot hide the old path forever.
func TestRenameShadowExpires(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	runtime := NewVisibility(overlay, deleteState, v, nil)
	runtime.AddRenameOverlay("/a.txt", "/b.txt", "old-id", false)

	overlay.mu.Lock()
	op := overlay.renameOverlays["/a.txt"]
	op.createdAt = time.Now().Add(-2 * RenameShadowTTL)
	overlay.renameOverlays["/a.txt"] = op
	overlay.mu.Unlock()

	if runtime.IsHidden("/a.txt") {
		t.Fatal("expired shadow must stop hiding the old path")
	}
	if _, ok := overlay.renameOverlays["/a.txt"]; ok {
		t.Fatal("expired shadow must be dropped")
	}
}
