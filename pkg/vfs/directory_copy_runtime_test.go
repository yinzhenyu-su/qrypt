package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSDirectoryCopyRuntimePreparesLocalState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDirectoryCopyRuntime(fs)
	fs.view.mu.Lock()
	fs.view.entries.Set("/dir", drive.Entry{ID: "dir", Name: "dir", IsDir: true})
	fs.view.entries.Set("/dir/remote.txt", drive.Entry{ID: "remote", Name: "remote.txt"})
	fs.view.entries.Set("/dir/local.txt", drive.Entry{ID: "local", Name: "local.txt"})
	fs.view.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Hour)}
	fs.view.mu.Unlock()

	runtime.PrepareLocalDirectoryCopy("/dir", map[string]time.Time{"remote.txt": time.Now().Add(time.Hour)})
	fs.view.mu.RLock()
	_, remoteCached := fs.view.entries.Get("/dir/remote.txt")
	_, localCached := fs.view.entries.Get("/dir/local.txt")
	_, listCached := fs.view.lists["/dir"]
	fs.view.mu.RUnlock()
	if remoteCached || localCached || listCached {
		t.Fatalf("remoteCached=%v localCached=%v listCached=%v, want child entries and list invalidated", remoteCached, localCached, listCached)
	}
	if !fs.isCopyHidden("/dir/remote.txt") || !fs.isCopyHidden("/dir/local.txt") {
		t.Fatal("copy-hidden overlay missing expected children")
	}
	if !fs.isRecentLocalDir("/dir") {
		t.Fatal("directory was not marked local")
	}
}
