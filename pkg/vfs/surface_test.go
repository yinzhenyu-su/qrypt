package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func newSurfaceTestVFS(t *testing.T) *VFS {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := New(localfs.New(dir), Options{Name: "loc", StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(context.Background()) })
	return fs
}

// TestBenchTargetResolution covers the mounted/mobile benchmark entry point:
// a probe asks for a mount and path, and the target carries both the driver
// entry and the driver used for direct reads.
func TestBenchTargetResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newSurfaceTestVFS(t)

	target, err := fs.BenchTarget(ctx, "loc", "/f.bin")
	if err != nil {
		t.Fatalf("bench target: %v", err)
	}
	if target.Entry.Name != "f.bin" || target.Entry.Size != int64(len("payload")) || target.Driver == nil {
		t.Fatalf("target = %+v", target)
	}
	if _, err := fs.BenchTarget(ctx, "other", "/f.bin"); err == nil {
		t.Fatal("bench target accepted the wrong mount")
	}
	if _, err := fs.BenchTarget(ctx, "loc", "/missing.bin"); err == nil {
		t.Fatal("bench target accepted a missing path")
	}

	ns, err := NewNamespace([]Mount{{Name: "loc", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })
	if _, err := ns.BenchTarget(ctx, "loc", "/f.bin"); err != nil {
		t.Fatalf("namespace bench target: %v", err)
	}
	if _, err := ns.BenchTarget(ctx, "", "/f.bin"); err == nil {
		t.Fatal("namespace bench target accepted an empty mount")
	}
	if _, err := ns.BenchTarget(ctx, "other", "/f.bin"); err == nil {
		t.Fatal("namespace bench target accepted an unknown mount")
	}
}

// TestWaitUploadsIdleDrains covers the idle waiter used by fs sync and tests:
// a drained mount returns immediately, a namespace waits for all mounts, and
// nil receivers are safe.
func TestWaitUploadsIdleDrains(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := newSurfaceTestVFS(t)
	fs.Start(ctx)
	if err := fs.WaitUploadsIdle(ctx); err != nil {
		t.Fatalf("idle VFS: %v", err)
	}
	var nilVFS *VFS
	if err := nilVFS.WaitUploadsIdle(ctx); err != nil {
		t.Fatalf("nil VFS: %v", err)
	}

	ns, err := NewNamespace([]Mount{{Name: "loc", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close(context.Background()) })
	if err := ns.WaitUploadsIdle(ctx); err != nil {
		t.Fatalf("idle namespace: %v", err)
	}
	var nilNS *Namespace
	if err := nilNS.WaitUploadsIdle(ctx); err != nil {
		t.Fatalf("nil namespace: %v", err)
	}
}

// TestTaskSourceEmptyVFS covers the uniform task source a drained mount
// exposes: listing is empty, lookups report not found, and dismiss is a no-op.
func TestTaskSourceEmptyVFS(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := newSurfaceTestVFS(t)
	fs.Start(ctx)
	source := fs.TaskSource()

	items, err := source.ListTasks(ctx, task.Filter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("list tasks = %v, %v", items, err)
	}
	if _, err := source.GetTask(ctx, "missing"); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("get missing task = %v, want task not found", err)
	}
	for name, fn := range map[string]func(context.Context, string) error{
		"cancel": source.CancelTask,
		"retry":  source.RetryTask,
	} {
		if err := fn(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s missing task = %v, want not found", name, err)
		}
	}
	dismissible, ok := source.(interface {
		DismissTask(context.Context, string) error
	})
	if !ok {
		t.Fatal("task source does not expose dismiss")
	}
	if err := dismissible.DismissTask(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dismiss missing task = %v, want not found", err)
	}
	canceled, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()
	if _, err := source.ListTasks(canceled, task.Filter{}); err == nil {
		t.Fatal("list tasks ignored a canceled context")
	}
}
