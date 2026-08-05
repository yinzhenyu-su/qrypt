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

	// The upload engine deletes the staging file asynchronously after the
	// backend file becomes visible (Chtimes lands before the staging
	// removal). Wait for the staging subdirectory to empty out so the test
	// TempDir cleanup does not race the removal.
	stagingDir := filepath.Join(storage, "staging")
	cleanupDeadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(stagingDir)
		if err == nil && len(entries) == 0 {
			break
		}
		if time.Now().After(cleanupDeadline) {
			t.Fatalf("staging never cleared in %s: %v", stagingDir, entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
