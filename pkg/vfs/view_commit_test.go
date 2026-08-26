package vfs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newViewCommitVFS builds a VFS over the fake driver with a small root
// tree for view-commit semantics tests.
func newViewCommitVFS(t *testing.T) *VFS {
	t.Helper()
	drv := drive.NewFakeDriver()
	if err := drv.Seed(map[string]string{
		"a.txt":      "alpha",
		"b.txt":      "beta",
		"dir1/c.txt": "gamma",
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

// TestViewCommitRemoteChildrenSemantics exercises the folded view commit on
// the real vfsListingView: remote children are cached, invisible nodes are
// filtered, local children are merged, local modtimes override remote ones,
// and rename overlays are applied - all through one call.
func TestViewCommitRemoteChildrenSemantics(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	// Prepare view state: a local-only child, a deleted remote child, and a
	// local modtime for a remote child. Mkdir writes a local entry into the
	// view (Create only stages a pending upload, which the lister merges
	// separately from the view commit).
	if _, err := fs.Mkdir(context.Background(), "/localdir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.resolve(context.Background(), "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(context.Background(), "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetModTime(context.Background(), "/a.txt", time.Unix(1234567890, 0)); err != nil {
		t.Fatal(err)
	}

	// A fresh remote listing for "/": a.txt present, b.txt still listed
	// remotely (deleted locally), local.txt absent remotely.
	remote := []drive.Entry{
		{ID: "id-a", Name: "a.txt", Size: 5},
		{ID: "id-b", Name: "b.txt", Size: 4},
	}
	got := view.CommitRemoteChildren("/", remote, time.Now().Add(time.Minute))

	names := make(map[string]drive.Entry, len(got))
	for _, e := range got {
		names[e.Name] = e
	}
	if _, ok := names["b.txt"]; ok {
		t.Error("deleted remote child b.txt is visible in the committed view")
	}
	// local.txt must be merged in from the local view.
	if _, ok := names["localdir"]; !ok {
		t.Error("local child localdir not merged into the committed view")
	}
	// a.txt modtime must be overridden by the local modtime.
	if e := names["a.txt"]; !e.ModTime.Equal(time.Unix(1234567890, 0)) {
		t.Errorf("a.txt modtime = %v, want local override 1234567890", e.ModTime)
	}
	// The view cache must now serve "/" from the committed listing.
	if cached, ok := view.FreshListCache("/", time.Now().Add(30*time.Second)); !ok {
		t.Error("committed listing not served from the fresh-list cache")
	} else {
		found := false
		for _, e := range cached {
			if e.Name == "localdir" {
				found = true
			}
		}
		if !found {
			t.Error("cached listing lost the merged local child")
		}
	}
}

// TestViewCommitRemoteChildrenDoesNotMutateInput: the committed view must
// not surprise the caller by mutating the input slice (fresh remote data).
func TestViewCommitRemoteChildrenDoesNotMutateInput(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	remote := []drive.Entry{
		{ID: "id-a", Name: "a.txt", Size: 5},
		{ID: "id-b", Name: "b.txt", Size: 4},
	}
	before := make([]drive.Entry, len(remote))
	copy(before, remote)

	_ = view.CommitRemoteChildren("/", remote, time.Now().Add(time.Minute))

	if len(remote) != len(before) {
		t.Fatalf("input slice length changed: %d -> %d", len(before), len(remote))
	}
	for i := range before {
		if remote[i].Name != before[i].Name || remote[i].ID != before[i].ID || remote[i].Size != before[i].Size {
			t.Fatalf("input entry %d mutated: got %+v want %+v", i, remote[i], before[i])
		}
	}
}

// TestViewCommitRemoteChildrenRenameOverlay: after a rename, the overlay is
// updated and the committed view reflects the new name.
func TestViewCommitRemoteChildrenRenameOverlay(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	// Remote rename a.txt -> renamed.txt.
	if err := fs.Rename(context.Background(), "/a.txt", "/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	// The remote driver still has a.txt; the overlay should hide the old
	// name and surface the new one once the remote lists renamed.txt.
	remote := []drive.Entry{
		{ID: "id-a", Name: "a.txt", Size: 5},
		{ID: "id-r", Name: "renamed.txt", Size: 5},
	}
	got := view.CommitRemoteChildren("/", remote, time.Now().Add(time.Minute))

	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if names["a.txt"] {
		t.Error("old name a.txt still visible after rename overlay commit")
	}
	if !names["renamed.txt"] {
		t.Error("new name renamed.txt not visible after rename overlay commit")
	}
}

// TestViewCommitRemoteChildrenHidesUnavailable: an explicitly hidden
// (copy-hidden) child stays invisible in the committed view.
func TestViewCommitRemoteChildrenHidesUnavailable(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	newVFSVisibilityRuntime(fs).SetCopyHidden("/", map[string]time.Time{"dir1": time.Now().Add(time.Minute)})
	remote := []drive.Entry{
		{ID: "id-d", Name: "dir1", IsDir: true},
		{ID: "id-a", Name: "a.txt", Size: 5},
	}
	got := view.CommitRemoteChildren("/", remote, time.Now().Add(time.Minute))
	for _, e := range got {
		if e.Name == "dir1" {
			t.Error("copy-hidden child dir1 is visible in the committed view")
		}
		if strings.HasPrefix(e.Name, "._") {
			t.Errorf("apple metadata child %s is visible in the committed view", e.Name)
		}
	}
}

// TestProjectChildrenSemantics: ProjectChildren applies the CURRENT
// visibility state to a snapshot without touching the overlay or cache:
// deleted/hidden nodes filtered, local children merged, local modtimes
// overriding remote ones, and the input slice untouched.
func TestProjectChildrenSemantics(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	if _, err := fs.Mkdir(context.Background(), "/localdir"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(context.Background(), "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetModTime(context.Background(), "/a.txt", time.Unix(1234567890, 0)); err != nil {
		t.Fatal(err)
	}
	newVFSVisibilityRuntime(fs).SetCopyHidden("/", map[string]time.Time{"dir1": time.Now().Add(time.Minute)})

	// A stale snapshot fetched earlier (e.g. an in-flight waiter's copy).
	snapshot := []drive.Entry{
		{ID: "id-a", Name: "a.txt", Size: 5},
		{ID: "id-b", Name: "b.txt", Size: 4},
		{ID: "id-d", Name: "dir1", IsDir: true},
	}
	got := view.ProjectChildren("/", snapshot)

	names := make(map[string]drive.Entry, len(got))
	for _, e := range got {
		names[e.Name] = e
	}
	if _, ok := names["b.txt"]; ok {
		t.Error("deleted b.txt visible after projection")
	}
	if _, ok := names["dir1"]; ok {
		t.Error("copy-hidden dir1 visible after projection")
	}
	if _, ok := names["localdir"]; !ok {
		t.Error("local child localdir not merged by projection")
	}
	if e := names["a.txt"]; !e.ModTime.Equal(time.Unix(1234567890, 0)) {
		t.Errorf("a.txt modtime = %v, want local override", e.ModTime)
	}
	// Input must be untouched.
	if len(snapshot) != 3 || snapshot[0].Name != "a.txt" || snapshot[1].Name != "b.txt" {
		t.Fatalf("input slice mutated by projection: %+v", snapshot)
	}
}

// TestProjectChildrenDoesNotTouchCache: projection must not commit or
// invalidate the fresh-list cache.
func TestProjectChildrenDoesNotTouchCache(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)

	// Commit a listing so the cache is warm, then project a different
	// snapshot: the cache must keep serving the committed listing.
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-a", Name: "a.txt", Size: 5}}, time.Now().Add(time.Minute))
	_ = view.ProjectChildren("/", []drive.Entry{{ID: "id-x", Name: "x.txt", Size: 1}})
	if cached, ok := view.FreshListCache("/", time.Now().Add(30*time.Second)); !ok {
		t.Fatal("projection invalidated the fresh-list cache")
	} else {
		for _, e := range cached {
			if e.Name == "x.txt" {
				t.Fatal("projection wrote into the fresh-list cache")
			}
		}
	}
}

// TestOwnerWaiterConcurrentProjection: an owner holding a slow remote fetch
// while multiple waiters project the snapshot concurrently with view-state
// mutations must be race-free. The fake driver's Delay makes the owner hold
// the list load long enough for waiters to pile up.
func TestOwnerWaiterConcurrentProjection(t *testing.T) {
	drv := drive.NewFakeDriver(func(d *drive.FakeDriver) { d.Delay = 2 * time.Millisecond })
	if err := drv.Seed(map[string]string{"a.txt": "alpha", "b.txt": "beta"}); err != nil {
		t.Fatal(err)
	}
	fs, err := New(drv, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer func() { _ = fs.Close(context.Background()) }()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = fs.List(ctx, "/")
			}
		}()
	}
	// Mutate view state while listings project snapshots.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = fs.SetModTime(ctx, "/a.txt", time.Unix(int64(j), 0))
				_, _ = fs.Mkdir(ctx, "/d")
				_ = fs.Remove(ctx, "/d")
			}
		}()
	}
	wg.Wait()
}

// TestViewSeparatesVisibilityFromCacheIdentity: the real vfsListingView
// keeps visibility (IsUnavailable) and cache identity (Entry) independent:
// an unresolved path is an entry miss but NOT unavailable.
func TestViewSeparatesVisibilityFromCacheIdentity(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)
	if view.IsUnavailable("/uncached") {
		t.Error("visible unresolved path reported unavailable")
	}
	if _, ok := view.Entry("/uncached"); ok {
		t.Error("unresolved path should be an entry-cache miss")
	}
	// A resolved+committed child has identity.
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-a", Name: "a.txt", Size: 5}}, time.Now().Add(time.Minute))
	if _, ok := view.Entry("/a.txt"); !ok {
		t.Error("committed child should have entry identity")
	}
}
