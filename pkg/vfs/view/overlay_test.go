package view

import (
	"strconv"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestIsDeletedUsesDirAncestorIndex(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
	runtime.MarkDeleted("/a/b", drive.Entry{ID: "b", IsDir: true})
	runtime.MarkDeleted("/plain.txt", drive.Entry{ID: "p"})

	cases := map[string]bool{
		"/a/b":         true,  // the deleted dir itself
		"/a/b/c.txt":   true,  // shadowed child
		"/a/b/c/d.txt": true,  // shadowed grandchild
		"/plain.txt":   true,  // deleted file
		"/a":           false, // parent of deleted dir is alive
		"/a2/b":        false, // prefix sibling is not an ancestor
		"/other.txt":   false,
	}
	for path, want := range cases {
		if got := runtime.IsDeleted(path); got != want {
			t.Errorf("isDeleted(%q) = %v, want %v", path, got, want)
		}
	}

	// Removing the dir entry must stop shadowing children.
	overlay.mu.Lock()
	overlay.removeDeleted("/a/b")
	overlay.mu.Unlock()
	if runtime.IsDeleted("/a/b/c.txt") {
		t.Error("isDeleted still true after dir overlay removed")
	}
	if !runtime.IsDeleted("/plain.txt") {
		t.Error("file overlay lost")
	}
}

func TestRestoreDeletedAncestorPicksDeepest(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
	runtime.MarkDeleted("/a", drive.Entry{ID: "a", IsDir: true})
	runtime.MarkDeleted("/a/b", drive.Entry{ID: "b", IsDir: true})

	restored, ok := runtime.RestoreDeletedPath("/a/b") // exact restore of the dir itself
	if !ok || restored.ID != "b" {
		t.Fatalf("RestoreDeletedPath(/a/b) = %+v ok=%v, want dir b", restored, ok)
	}
	// /a is still deleted, so children of /a/b remain shadowed by /a.
	if !runtime.IsDeleted("/a/b/c.txt") {
		t.Error("children must stay hidden under still-deleted ancestor /a")
	}

	// Ancestor restore via a child path picks the deepest deleted dir.
	runtime.MarkDeleted("/x", drive.Entry{ID: "x", IsDir: true})
	runtime.MarkDeleted("/x/y", drive.Entry{ID: "y", IsDir: true})
	runtime.RestoreDeletedAncestor("/x/y/z.txt")
	overlay.mu.Lock()
	_, yStillDeleted := overlay.deleted["/x/y"]
	_, xStillDeleted := overlay.deleted["/x"]
	overlay.mu.Unlock()
	if yStillDeleted {
		t.Error("deepest ancestor /x/y should be restored")
	}
	if !xStillDeleted {
		t.Error("outer ancestor /x must stay deleted")
	}
}

func TestIsHiddenUsesRenameDirIndex(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	runtime := NewVisibility(overlay, tasks, v, nil)
	runtime.AddRenameOverlay("/old", "/new", "id1", true)           // recursive
	runtime.AddRenameOverlay("/file.txt", "/ren.txt", "id2", false) // file op

	cases := map[string]bool{
		"/old":         true,
		"/old/sub":     true,
		"/old/sub/f":   true,
		"/file.txt":    true,
		"/new":         false,
		"/oldx":        false,
		"/other/f.txt": false,
	}
	for path, want := range cases {
		if got := runtime.IsHidden(path); got != want {
			t.Errorf("IsHidden(%q) = %v, want %v", path, got, want)
		}
	}

	// A recursive overlay prunes nested overlays under it (the path itself
	// stays shadowed through the /old overlay).
	runtime.AddRenameOverlay("/old/sub2", "/n2", "id3", true)
	runtime.AddRenameOverlay("/old", "/newer", "id1", true) // re-add recursive parent
	overlay.mu.Lock()
	_, sub2Pruned := overlay.renameOverlays["/old/sub2"]
	overlay.mu.Unlock()
	if sub2Pruned {
		t.Error("nested overlay /old/sub2 should be pruned by recursive /old overlay")
	}

	// MarkDeleted drops the rename overlay for the same path.
	runtime.MarkDeleted("/file.txt", drive.Entry{ID: "id2"})
	if runtime.IsHidden("/file.txt") {
		t.Error("rename overlay must be dropped when path is deleted")
	}
}

// BenchmarkIsDeletedManyOverlays guards the index: with thousands of file
// overlays plus a few dir overlays, IsDeleted on an unrelated path must stay
// O(depth), not O(overlays).
func BenchmarkIsDeletedManyOverlays(b *testing.B) {
	overlay, tasks := NewOverlayTasks()
	v := NewView("0", time.Now(), overlay)
	runtime := NewVisibility(overlay, tasks, v, nil)
	for i := range 5000 {
		runtime.MarkDeleted("/files/f"+strconv.Itoa(i)+".txt", drive.Entry{ID: "f"})
	}
	runtime.MarkDeleted("/gone/dir", drive.Entry{ID: "d", IsDir: true})

	for b.Loop() {
		if runtime.IsDeleted("/alive/path/file.txt") {
			b.Fatal("unexpected delete")
		}
	}
}
