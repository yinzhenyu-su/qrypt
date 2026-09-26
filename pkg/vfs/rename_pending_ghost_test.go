package vfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestVFSRenamePendingOnlyDropsGhostTempName covers the downloader flow the
// remote-counterpart tests miss: the temp name is renamed before any
// generation uploads, so the rename takes the pending-only path. The source
// path then never existed on the backend - only as a local pending
// projection - and the view entry cache must stop serving that old identity.
// Otherwise LocalChildren re-injects the .qkdownloading name into every later
// listing, a stat of the ghost succeeds with the staging FID, and a read of it
// fails against the missing backend object.
func TestVFSRenamePendingOnlyDropsGhostTempName(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	// Pre-create the directory on the backend so it is never a "recent local
	// dir": that marker makes resolve short-circuit without listing the parent.
	if err := os.MkdirAll(filepath.Join(remote, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   200 * time.Millisecond,
		RootID:        remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if err := fs.Create(ctx, "/d/x.mp4.qkdownloading"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/d/x.mp4.qkdownloading", []byte("part-one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/d/x.mp4.qkdownloading"); err != nil {
		t.Fatal(err)
	}
	// A lookup of a missing sibling lists the parent and commits every child -
	// including the pending .qkdownloading projection - into the view entry
	// cache, exactly as macOS sidecar lookups do while downloading.
	if _, err := fs.Stat(ctx, "/d/does-not-exist"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("stat missing sibling err = %v, want ErrNotFound", err)
	}

	if err := fs.Rename(ctx, "/d/x.mp4.qkdownloading", "/d/x.mp4"); err != nil {
		t.Fatal(err)
	}
	// The pending record now lives at the final name; the old name must be
	// gone even before the upload lands.
	if entries, err := fs.List(ctx, "/d"); err != nil {
		t.Fatal(err)
	} else if namesOf(entries) != "x.mp4" {
		t.Fatalf("listed %q right after rename, want %q", namesOf(entries), "x.mp4")
	}
	waitNoPending(t, fs)
	assertListNames(t, fs, "/d", "x.mp4")
}
