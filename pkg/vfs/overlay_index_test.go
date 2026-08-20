package vfs

import (
	"strconv"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func newOverlayTestVFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestIsDeletedUsesDirAncestorIndex(t *testing.T) {
	fs := newOverlayTestVFS(t)
	fs.markDeleted("/a/b", drive.Entry{ID: "b", IsDir: true})
	fs.markDeleted("/plain.txt", drive.Entry{ID: "p"})

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
		if got := fs.isDeleted(path); got != want {
			t.Errorf("isDeleted(%q) = %v, want %v", path, got, want)
		}
	}

	// Removing the dir entry must stop shadowing children.
	fs.view.overlay.mu.Lock()
	fs.view.overlay.removeDeleted("/a/b")
	fs.view.overlay.mu.Unlock()
	if fs.isDeleted("/a/b/c.txt") {
		t.Error("isDeleted still true after dir overlay removed")
	}
	if !fs.isDeleted("/plain.txt") {
		t.Error("file overlay lost")
	}
}

func TestRestoreDeletedAncestorPicksDeepest(t *testing.T) {
	fs := newOverlayTestVFS(t)
	fs.markDeleted("/a", drive.Entry{ID: "a", IsDir: true})
	fs.markDeleted("/a/b", drive.Entry{ID: "b", IsDir: true})

	restored, ok := fs.restoreDeletedPath("/a/b") // exact restore of the dir itself
	if !ok || restored.ID != "b" {
		t.Fatalf("restoreDeletedPath(/a/b) = %+v ok=%v, want dir b", restored, ok)
	}
	// /a is still deleted, so children of /a/b remain shadowed by /a.
	if !fs.isDeleted("/a/b/c.txt") {
		t.Error("children must stay hidden under still-deleted ancestor /a")
	}

	// Ancestor restore via a child path picks the deepest deleted dir.
	fs.markDeleted("/x", drive.Entry{ID: "x", IsDir: true})
	fs.markDeleted("/x/y", drive.Entry{ID: "y", IsDir: true})
	fs.restoreDeletedAncestor("/x/y/z.txt")
	fs.view.overlay.mu.Lock()
	_, yStillDeleted := fs.view.overlay.deleted["/x/y"]
	_, xStillDeleted := fs.view.overlay.deleted["/x"]
	fs.view.overlay.mu.Unlock()
	if yStillDeleted {
		t.Error("deepest ancestor /x/y should be restored")
	}
	if !xStillDeleted {
		t.Error("outer ancestor /x must stay deleted")
	}
}

func TestIsHiddenUsesRenameDirIndex(t *testing.T) {
	fs := newOverlayTestVFS(t)
	hidden := func(path string) bool { return newVFSVisibilityRuntime(fs).IsHidden(path) }
	fs.addOverlay("/old", "/new", "id1", true)           // recursive
	fs.addOverlay("/file.txt", "/ren.txt", "id2", false) // file op

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
		if got := hidden(path); got != want {
			t.Errorf("IsHidden(%q) = %v, want %v", path, got, want)
		}
	}

	// A recursive overlay prunes nested overlays under it (checked via the
	// maps: the path itself stays shadowed through the /old overlay).
	fs.addOverlay("/old/sub2", "/n2", "id3", true)
	fs.addOverlay("/old", "/newer", "id1", true) // re-add recursive parent
	fs.view.overlay.mu.Lock()
	_, sub2Pruned := fs.view.overlay.renameOverlays["/old/sub2"]
	fs.view.overlay.mu.Unlock()
	if sub2Pruned {
		t.Error("nested overlay /old/sub2 should be pruned by recursive /old overlay")
	}

	// MarkDeleted drops the rename overlay for the same path.
	fs.markDeleted("/file.txt", drive.Entry{ID: "id2"})
	if hidden("/file.txt") {
		t.Error("rename overlay must be dropped when path is deleted")
	}
}

// BenchmarkIsDeletedManyOverlays guards the index: with thousands of file
// overlays plus a few dir overlays, IsDeleted on an unrelated path must stay
// O(depth), not O(overlays).
func BenchmarkIsDeletedManyOverlays(b *testing.B) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	for i := range 5000 {
		fs.markDeleted("/files/f"+strconv.Itoa(i)+".txt", drive.Entry{ID: "f"})
	}
	fs.markDeleted("/gone/dir", drive.Entry{ID: "d", IsDir: true})

	for b.Loop() {
		if fs.isDeleted("/alive/path/file.txt") {
			b.Fatal("unexpected delete")
		}
	}
}
