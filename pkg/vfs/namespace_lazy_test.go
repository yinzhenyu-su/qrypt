package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestLazyNamespaceOpensOnlyTouchedMount(t *testing.T) {
	t.Parallel()
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "file.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opensA, opensB atomic.Int64
	ns, err := NewLazyNamespace([]LazyMount{
		{Name: "a", Open: func(context.Context) (*VFS, error) {
			opensA.Add(1)
			return New(localfs.New(dirA), Options{Name: "a", StorageDir: t.TempDir()})
		}},
		{Name: "b", Open: func(context.Context) (*VFS, error) {
			opensB.Add(1)
			return New(localfs.New(dirB), Options{Name: "b", StorageDir: t.TempDir()})
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })

	root, err := ns.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != 2 || opensA.Load() != 0 || opensB.Load() != 0 {
		t.Fatalf("root list = %v, opens a=%d b=%d; want both mounts listed without opening", names(root), opensA.Load(), opensB.Load())
	}
	if mounts := ns.Mounts(); len(mounts) != 2 {
		t.Fatalf("Mounts = %v, want lazy entries visible", mounts)
	}

	// Only the touched mount is opened; the sibling stays lazy.
	entry, err := ns.Stat(context.Background(), "/a/file.txt")
	if err != nil {
		t.Fatalf("stat /a/file.txt: %v", err)
	}
	if entry.Name != "file.txt" || opensA.Load() != 1 || opensB.Load() != 0 {
		t.Fatalf("entry=%+v opens a=%d b=%d; want only a opened", entry, opensA.Load(), opensB.Load())
	}
	if _, err := ns.Stat(context.Background(), "/a/file.txt"); err != nil {
		t.Fatal(err)
	}
	if opensA.Load() != 1 {
		t.Fatalf("second access reopened mount a (%d opens)", opensA.Load())
	}
	if _, err := ns.List(context.Background(), "/b"); err != nil {
		t.Fatalf("list /b: %v", err)
	}
	if opensB.Load() != 1 {
		t.Fatalf("touching b should open it once, got %d", opensB.Load())
	}
}

func TestLazyNamespaceConcurrentAccessOpensOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ns, err := NewLazyNamespace([]LazyMount{{Name: "a", Open: func(context.Context) (*VFS, error) {
		opens.Add(1)
		return New(localfs.New(dir), Options{Name: "a", StorageDir: t.TempDir()})
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ns.Stat(context.Background(), "/a/f.txt"); err != nil {
				t.Errorf("stat: %v", err)
			}
		}()
	}
	wg.Wait()
	if opens.Load() != 1 {
		t.Fatalf("concurrent access opened mount %d times, want 1", opens.Load())
	}
}

func TestLazyNamespaceCachesOpenFailure(t *testing.T) {
	t.Parallel()
	openErr := errors.New("backend offline")
	var attempts atomic.Int64
	ns, err := NewLazyNamespace([]LazyMount{{Name: "a", Open: func(context.Context) (*VFS, error) {
		attempts.Add(1)
		return nil, openErr
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })
	for i := 0; i < 2; i++ {
		if _, err := ns.Stat(context.Background(), "/a/f.txt"); !errors.Is(err, openErr) {
			t.Fatalf("stat %d error = %v, want %v", i, err, openErr)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("open attempts = %d, want cached failure", attempts.Load())
	}
}

func TestLazyNamespaceMountHealthOpensNamedMount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var opens atomic.Int64
	ns, err := NewLazyNamespace([]LazyMount{{Name: "a", Open: func(context.Context) (*VFS, error) {
		opens.Add(1)
		return New(localfs.New(dir), Options{Name: "a", StorageDir: t.TempDir()})
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })

	health, err := ns.MountHealth(context.Background(), "a")
	if err != nil {
		t.Fatalf("health a: %v", err)
	}
	if len(health) != 1 || health[0].Mount != "a" {
		t.Fatalf("health = %+v, want mount a", health)
	}
	if opens.Load() != 1 {
		t.Fatalf("named health opened mount %d times, want 1", opens.Load())
	}
	// The mount is now registered; a second probe must not re-run the opener.
	if _, err := ns.MountHealth(context.Background(), "a"); err != nil {
		t.Fatalf("second health a: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("second health reopened mount (%d opens)", opens.Load())
	}
	if _, err := ns.MountHealth(context.Background(), "missing"); err == nil {
		t.Fatal("health for unknown mount succeeded, want not found")
	}
}

func TestLazyNamespaceMountHealthReportsInitFailure(t *testing.T) {
	t.Parallel()
	openErr := errors.New("provider offline")
	var attempts atomic.Int64
	ns, err := NewLazyNamespace([]LazyMount{{Name: "a", Open: func(context.Context) (*VFS, error) {
		attempts.Add(1)
		return nil, openErr
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })

	for i := 0; i < 2; i++ {
		_, err := ns.MountHealth(context.Background(), "a")
		if !errors.Is(err, openErr) {
			t.Fatalf("health %d error = %v, want %v", i, err, openErr)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("health opened failing mount %d times, want cached failure", attempts.Load())
	}
}

func names(entries []drive.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}
