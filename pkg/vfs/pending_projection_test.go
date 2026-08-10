package vfs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newPendingViewVFS builds a VFS over the fake driver with remote content
// and a fast upload delay disabled (so pending records persist).
func newPendingViewVFS(t *testing.T) *VFS {
	t.Helper()
	drv := drive.NewFakeDriver()
	if err := drv.Seed(map[string]string{
		"remote.txt": "remote",
		"both.txt":   "remote-version",
	}); err != nil {
		t.Fatal(err)
	}
	fs, err := New(drv, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(context.Background()) })
	return fs
}

// TestPendingOnlyFileVisible: a pending upload with no remote counterpart
// appears in the listing with its FID/size/modtime mapping.
func TestPendingOnlyFileVisible(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	if err := fs.Create(ctx, "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/new.txt", []byte("staged"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/new.txt"); err != nil {
		t.Fatal(err)
	}

	view := newVFSListingView(fs)
	got := view.ProjectChildren("/", []drive.Entry{})
	if len(got) != 1 || got[0].Name != "new.txt" {
		t.Fatalf("projected = %+v, want [new.txt]", got)
	}
	if got[0].Size != int64(len("staged")) {
		t.Errorf("pending size = %d, want %d", got[0].Size, len("staged"))
	}
	if got[0].ID == "" {
		t.Error("pending FID not mapped into the entry ID")
	}
}

// TestPendingDeduplicatesWithRemote: a pending upload with the same name as
// a remote child appears exactly once; the pending entry wins (remote
// entry is shadowed, matching the pre-split behavior).
func TestPendingDeduplicatesWithRemote(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	if err := fs.Create(ctx, "/both.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/both.txt", []byte("local-version"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/both.txt"); err != nil {
		t.Fatal(err)
	}

	view := newVFSListingView(fs)
	got := view.ProjectChildren("/", []drive.Entry{{ID: "remote-id", Name: "both.txt", Size: 13}})
	count := 0
	for _, e := range got {
		if e.Name == "both.txt" {
			count++
			if e.Size != int64(len("local-version")) {
				t.Errorf("both.txt size = %d, want pending size %d", e.Size, len("local-version"))
			}
		}
	}
	if count != 1 {
		t.Fatalf("both.txt appears %d times, want exactly 1", count)
	}
}

// TestDeletedPendingInvisible: a pending upload under a deleted path is
// hidden.
func TestDeletedPendingInvisible(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	if err := fs.Create(ctx, "/ghost.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/ghost.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/ghost.txt"); err != nil {
		t.Fatal(err)
	}
	// Remove the file: the delete overlay marks /ghost.txt deleted.
	if err := fs.Remove(ctx, "/ghost.txt"); err != nil {
		t.Fatal(err)
	}

	view := newVFSListingView(fs)
	got := view.ProjectChildren("/", []drive.Entry{})
	for _, e := range got {
		if e.Name == "ghost.txt" {
			t.Error("deleted pending upload still visible")
		}
	}
}

// TestPendingDynamicWithFreshCache: the fresh-list cache stores only the
// stable remote projection; a pending upload appearing or disappearing is
// reflected immediately without cache invalidation.
func TestPendingDynamicWithFreshCache(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	view := newVFSListingView(fs)

	// Commit a remote listing (cache now warm).
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-r", Name: "remote.txt", Size: 6}}, time.Now().Add(time.Minute))

	// No pending yet: cache serves remote.txt only.
	if got, ok := view.FreshListCache("/", time.Now().Add(30*time.Second)); !ok {
		t.Fatal("fresh cache missing after commit")
	} else if len(got) != 1 || got[0].Name != "remote.txt" {
		t.Fatalf("cached = %+v, want [remote.txt]", got)
	}

	// Create a pending upload: it must show up even though the cache is
	// still fresh (no invalidation happened).
	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("d"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if got, ok := view.FreshListCache("/", time.Now().Add(30*time.Second)); !ok {
		t.Fatal("cache expired unexpectedly")
	} else {
		names := map[string]bool{}
		for _, e := range got {
			names[e.Name] = true
		}
		if !names["draft.txt"] {
			t.Error("pending draft.txt not visible via still-fresh cache")
		}
		if !names["remote.txt"] {
			t.Error("remote.txt missing from cache projection")
		}
	}
}

// TestRemoteListExcludesPending: RemoteList bypasses the view and returns
// only driver data.
func TestRemoteListExcludesPending(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	if err := fs.Create(ctx, "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/new.txt", []byte("staged"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/new.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.RemoteList(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "new.txt" {
			t.Error("RemoteList includes pending upload")
		}
	}
}

// TestPendingConcurrentWithList: pending projection racing concurrent
// listings must be race-free.
func TestPendingConcurrentWithList(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				path := "/p" + strings.Repeat("x", n) + ".txt"
				_ = fs.Create(ctx, path)
				_, _ = fs.WriteAt(ctx, path, []byte("data"), 0)
				_ = fs.Flush(ctx, path)
				_, _ = fs.List(ctx, "/")
			}
		}(i)
	}
	wg.Wait()
}
