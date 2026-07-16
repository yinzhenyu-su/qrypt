package vfs_test

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSListCachesChildrenForStat(t *testing.T) {
	ctx := context.Background()
	drv := &countingListDriver{lists: map[string]int{}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/child.txt"); err != nil {
		t.Fatal(err)
	}
	if got := drv.listCount("0"); got != 1 {
		t.Fatalf("root list count = %d, want 1", got)
	}
}

func TestVFSStartDirectoryPrefetchWarmsRootChildDirs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
				{ID: "file", ParentID: "0", Name: "file.txt"},
			},
			"dir-a": {
				{ID: "nested", ParentID: "dir-a", Name: "nested.txt"},
			},
		},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	fs.StartDirectoryPrefetch(ctx)

	waitForCondition(t, func() bool {
		return drv.listCount("0") == 1 && drv.listCount("dir-a") == 1
	})
	if _, err := fs.List(ctx, "/a"); err != nil {
		t.Fatal(err)
	}
	if got := drv.listCount("dir-a"); got != 1 {
		t.Fatalf("prefetched child dir should be cached, list count = %d", got)
	}
}

func TestVFSListPrefetchesNextLevelChildDirs(t *testing.T) {
	ctx := context.Background()
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
			},
			"dir-a": {
				{ID: "dir-b", ParentID: "dir-a", Name: "b", IsDir: true},
			},
			"dir-b": {
				{ID: "leaf", ParentID: "dir-b", Name: "leaf.txt"},
			},
		},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.StartDirectoryPrefetch(ctx)
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		return drv.listCount("dir-a") == 1
	})

	if _, err := fs.List(ctx, "/a"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		return drv.listCount("dir-b") == 1
	})
	if got := drv.listCount("dir-b"); got != 1 {
		t.Fatalf("background prefetch should not recursively prefetch beyond direct children, got %d", got)
	}
}

func TestVFSListWaitsForInFlightDirectoryPrefetch(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
			},
			"dir-a": {
				{ID: "child", ParentID: "dir-a", Name: "child.txt"},
			},
		},
		entered: map[string]chan struct{}{"dir-a": entered},
		release: map[string]chan struct{}{"dir-a": release},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.StartDirectoryPrefetch(ctx)
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("directory prefetch did not enter child list")
	}

	listDone := make(chan []drive.Entry, 1)
	errDone := make(chan error, 1)
	go func() {
		entries, err := fs.List(ctx, "/a")
		if err != nil {
			errDone <- err
			return
		}
		listDone <- entries
	}()

	time.Sleep(50 * time.Millisecond)
	if got := drv.listCount("dir-a"); got != 1 {
		t.Fatalf("foreground list should share in-flight prefetch, list count = %d", got)
	}
	close(release)
	select {
	case err := <-errDone:
		t.Fatalf("foreground list failed: %v", err)
	case entries := <-listDone:
		if len(entries) != 1 || entries[0].Name != "child.txt" {
			t.Fatalf("foreground list entries = %+v", entries)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground list did not finish after prefetch was released")
	}
	if got := drv.listCount("dir-a"); got != 1 {
		t.Fatalf("shared list should only call backend once, got %d", got)
	}
}

func TestVFSForegroundListRetriesCanceledDirectoryPrefetch(t *testing.T) {
	prefetchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
			},
			"dir-a": {
				{ID: "child", ParentID: "dir-a", Name: "child.txt"},
			},
		},
		entered: map[string]chan struct{}{"dir-a": entered},
		release: map[string]chan struct{}{"dir-a": release},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.StartDirectoryPrefetch(prefetchCtx)
	if _, err := fs.List(prefetchCtx, "/"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("directory prefetch did not enter child list")
	}

	listDone := make(chan []drive.Entry, 1)
	errDone := make(chan error, 1)
	go func() {
		entries, err := fs.List(context.Background(), "/a")
		if err != nil {
			errDone <- err
			return
		}
		listDone <- entries
	}()

	waitForCondition(t, func() bool { return drv.listCount("dir-a") == 1 })
	cancel()
	waitForCondition(t, func() bool { return drv.listCount("dir-a") == 2 })
	close(release)
	select {
	case err := <-errDone:
		t.Fatalf("foreground list inherited prefetch error: %v", err)
	case entries := <-listDone:
		if len(entries) != 1 || entries[0].Name != "child.txt" {
			t.Fatalf("foreground list entries = %+v", entries)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground list did not retry after canceled prefetch")
	}
	if got := drv.listCount("dir-a"); got != 2 {
		t.Fatalf("foreground list should retry canceled prefetch, list count = %d", got)
	}
}

func TestVFSDirectoryPrefetchFallsBackAfterSessionContextCanceled(t *testing.T) {
	prefetchCtx, cancel := context.WithCancel(context.Background())
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
			},
			"dir-a": {
				{ID: "dir-b", ParentID: "dir-a", Name: "b", IsDir: true},
			},
			"dir-b": {
				{ID: "leaf", ParentID: "dir-b", Name: "leaf.txt"},
			},
		},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.StartDirectoryPrefetch(prefetchCtx)
	if _, err := fs.List(prefetchCtx, "/"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { return drv.listCount("dir-a") == 1 })
	cancel()

	if _, err := fs.List(context.Background(), "/a"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { return drv.listCount("dir-b") == 1 })
}

func TestVFSDirectoryPrefetchDiscardsStalePathAfterRename(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0": {
				{ID: "dir-a", ParentID: "0", Name: "a", IsDir: true},
			},
			"dir-a": {
				{ID: "child", ParentID: "dir-a", Name: "child.txt"},
			},
		},
		entered: map[string]chan struct{}{"dir-a": entered},
		release: map[string]chan struct{}{"dir-a": release},
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("directory prefetch did not enter child list")
	}

	if err := fs.Rename(ctx, "/a", "/renamed"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if _, err := fs.List(ctx, "/a"); err == nil {
		t.Fatal("old directory path should not be listable after rename")
	}
	if _, err := fs.Stat(ctx, "/a/child.txt"); err == nil {
		t.Fatal("stale prefetch resurrected child under old path")
	}
	if got := drv.listCount("dir-a"); got != 1 {
		t.Fatalf("old path should not be listed again after stale prefetch, got %d", got)
	}
}
