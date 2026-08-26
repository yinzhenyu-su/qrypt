package view

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newTestDomain builds an isolated composite domain for view-level tests:
// a fresh paired overlay/tasks state and a view over it. No VFS assembly.
func newTestDomain(t *testing.T) (*View, *Overlay, *Tasks) {
	t.Helper()
	overlay, tasks := NewOverlayTasks()
	return NewView("0", time.Now(), overlay), overlay, tasks
}

func TestRuntimeRebasesCachedPaths(t *testing.T) {
	v, _, _ := newTestDomain(t)
	runtime := NewRuntime(v)
	v.mu.Lock()
	v.entries.Set("/old/file.txt", drive.Entry{ID: "file", Name: "file.txt"})
	v.entries.Set("/other.txt", drive.Entry{ID: "other", Name: "other.txt"})
	runtime.RebaseCachedPathsLocked("/old", "/new")
	_, oldOK := v.entries.Get("/old/file.txt")
	_, newOK := v.entries.Get("/new/file.txt")
	_, otherOK := v.entries.Get("/other.txt")
	v.mu.Unlock()
	if oldOK || !newOK || !otherOK {
		t.Fatalf("old=%v new=%v other=%v", oldOK, newOK, otherOK)
	}
}

func TestRuntimeTracksRecentLocalDirs(t *testing.T) {
	v, _, _ := newTestDomain(t)
	runtime := NewRuntime(v)
	v.mu.Lock()
	runtime.MarkLocalDirLocked("/fresh")
	v.localDirs["/expired"] = time.Now().Add(-time.Minute)
	v.mu.Unlock()

	if !runtime.IsRecentLocalDir("/fresh") {
		t.Fatal("fresh local dir should be recent")
	}
	if runtime.IsRecentLocalDir("/expired") {
		t.Fatal("expired local dir should not be recent")
	}
}

func TestRuntimeMovesAndClearsLocalModTime(t *testing.T) {
	v, _, _ := newTestDomain(t)
	runtime := NewRuntime(v)
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

func TestRuntimeRefreshesListCache(t *testing.T) {
	v, _, _ := newTestDomain(t)
	runtime := NewRuntime(v)
	v.mu.Lock()
	v.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	v.mu.Unlock()

	runtime.RefreshPath("/dir")
	v.mu.RLock()
	_, ok := v.lists["/dir"]
	v.mu.RUnlock()
	if ok {
		t.Fatal("list cache should be refreshed")
	}
}

func TestRuntimeCommitsEntryLocalModTime(t *testing.T) {
	v, _, _ := newTestDomain(t)
	runtime := NewRuntime(v)
	modTime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	v.mu.Lock()
	v.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	v.mu.Unlock()

	runtime.CommitEntryLocalModTime("/dir/file.txt", drive.Entry{ID: "file", Name: "file.txt"}, modTime)
	v.mu.RLock()
	entry, entryOK := v.entries.Get("/dir/file.txt")
	_, listOK := v.lists["/dir"]
	v.mu.RUnlock()
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

func TestPrepareLocalDirectoryCopyHidesCachedChildren(t *testing.T) {
	v, overlay, _ := newTestDomain(t)
	runtime := NewRuntime(v)
	v.mu.Lock()
	v.entries.Set("/dir", drive.Entry{ID: "dir", Name: "dir", IsDir: true})
	v.entries.Set("/dir/remote.txt", drive.Entry{ID: "remote", Name: "remote.txt"})
	v.entries.Set("/dir/local.txt", drive.Entry{ID: "local", Name: "local.txt"})
	v.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Hour)}
	v.mu.Unlock()

	runtime.PrepareLocalDirectoryCopy("/dir", map[string]time.Time{"remote.txt": time.Now().Add(time.Hour)})
	v.mu.RLock()
	_, remoteCached := v.entries.Get("/dir/remote.txt")
	_, localCached := v.entries.Get("/dir/local.txt")
	_, listCached := v.lists["/dir"]
	v.mu.RUnlock()
	if remoteCached || localCached || listCached {
		t.Fatalf("remoteCached=%v localCached=%v listCached=%v, want child entries and list invalidated", remoteCached, localCached, listCached)
	}
	vis := NewVisibility(overlay, nil, v, nil)
	if !vis.IsCopyHidden("/dir/remote.txt") || !vis.IsCopyHidden("/dir/local.txt") {
		t.Fatal("copy-hidden overlay missing expected children")
	}
	if !runtime.IsRecentLocalDir("/dir") {
		t.Fatal("directory was not marked local")
	}
}
