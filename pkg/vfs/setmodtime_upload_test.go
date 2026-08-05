package vfs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestSetModTimeAppliesToUpload verifies that a SetModTime issued while the
// file is still pending is honored by the upload: the committed entry must
// carry the requested mtime, not the backend upload time.
func TestSetModTimeAppliesToUpload(t *testing.T) {
	remote := t.TempDir()
	driver := localfs.New(remote)
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(t.TempDir(), "cache")
	v, err := vfs.New(driver, vfs.Options{
		StorageDir:  storage,
		UploadDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer stopVFS(t, v)
	v.Start(ctx)
	defer cancel()

	const path = "/f.txt"
	if err := v.Create(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, path, []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := v.Flush(ctx, path); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1_700_000_000, 0)
	if err := v.SetModTime(ctx, path, fixed); err != nil {
		t.Fatalf("SetModTime while pending: %v", err)
	}

	// Wait until the backend file is visible AND its mtime is stable at the
	// requested value. localfs creates the file before the Chtimes call, so
	// "file visible" alone can race the mtime stamp.
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := driver.List(context.Background(), "")
		if err == nil {
			for _, e := range entries {
				if e.Name == "f.txt" && e.ModTime.Equal(fixed) {
					goto done
				}
			}
		}
		if time.Now().After(deadline) {
			found := ""
			if entries, err := driver.List(context.Background(), ""); err == nil {
				for _, e := range entries {
					if e.Name == "f.txt" {
						found = e.ModTime.String()
					}
				}
			}
			t.Fatalf("backend mtime never reached %v (last=%s)", fixed, found)
		}
		time.Sleep(10 * time.Millisecond)
	}
done:

	entry, err := v.Stat(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.ModTime.Equal(fixed) {
		t.Fatalf("committed mtime = %v, want %v", entry.ModTime, fixed)
	}

	// The upload engine finishes asynchronously inside the worker:
	// CommitUploadedEntry -> RemoveIfUnchanged (pending record) ->
	// RemoveStaging (staging file) -> observer.Finish (deferred) -> return.
	// A bare staging-directory poll is not enough: the worker is still
	// running its finish path after the staging file is gone, racing this
	// test's TempDir cleanup. waitNoPending covers the full drain (no
	// pending record AND no active worker).
	waitNoPending(t, v)

	// Double-check the staging file is really gone (RemoveStaging runs
	// inside the worker just before it returns).
	stagingDir := filepath.Join(storage, "staging")
	waitForCondition(t, func() bool {
		entries, err := os.ReadDir(stagingDir)
		return err == nil && len(entries) == 0
	})
}
