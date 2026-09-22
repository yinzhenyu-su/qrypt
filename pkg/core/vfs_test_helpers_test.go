package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// stopTestVFS synchronously drops a started VFS's cache files so a test
// TempDir cleanup never races the asynchronous shutdown. Pair it with the
// deferred context cancel (which stops upload workers) by adding this right
// after fs is created:
//
//	defer stopTestVFS(t, fs)
func stopTestVFS(t testing.TB, fs vfs.FileSystem) {
	t.Helper()
	if closer, ok := fs.(interface{ CloseReadCache() error }); ok {
		if err := closer.CloseReadCache(); err != nil {
			t.Logf("close read cache: %v", err)
		}
	}
	if clearer, ok := fs.(interface{ ClearReadCache() error }); ok {
		if err := clearer.ClearReadCache(); err != nil {
			t.Logf("clear read cache: %v", err)
		}
	}
}

// waitCoreTaskUntil blocks until cond holds on the task's current snapshot.
// Tests synchronize on machine state (task/item State); display labels like
// Phase are asserted explicitly afterwards, never used as barriers.
func waitCoreTaskUntil(t *testing.T, c *Core, id string, cond func(task.Task) bool) task.Task {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var last task.Task
	for time.Now().Before(deadline) {
		item, err := c.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		last = item
		if cond(item) {
			return item
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not satisfy the condition, last=%+v", id, last)
	return task.Task{}
}

// waitCoreTaskGone blocks until the task has been removed.
func waitCoreTaskGone(t *testing.T, c *Core, id string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := c.GetTask(ctx, id)
		if errors.Is(err, task.ErrNotFound) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s still present after the deadline, want removed", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hasItemInState reports whether any item tracking projection sits in state.
func hasItemInState(item task.Task, state task.ItemState) bool {
	for _, tracking := range item.Tracking.Items {
		if tracking.State == state {
			return true
		}
	}
	return false
}

// waitForTaskSnapshots blocks until the task's snapshot has been republished
// n times (UpdatedAt advances). In a paused steady state the progress ticker
// is the only republisher, so n observed republishes bound it having ticked
// n times — no gambling on a sleep "surely" outlasting it.
func waitForTaskSnapshots(t *testing.T, c *Core, id string, n int) {
	t.Helper()
	ctx := context.Background()
	last, err := c.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for seen := 0; seen < n; {
		if time.Now().After(deadline) {
			t.Fatalf("task %s published %d/%d snapshots before the deadline", id, seen, n)
		}
		time.Sleep(time.Millisecond)
		cur, err := c.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if cur.UpdatedAt.After(last.UpdatedAt) {
			seen++
		}
		last = cur
	}
}

// newTestCore builds a Core over a filesystem and registers cleanup that
// closes it, so the lazily-created task manager (whose runner goroutines
// derive from the manager context, not the caller's) always shuts down.
func newTestCore(t *testing.T, fs BuiltFileSystem) *Core {
	t.Helper()
	c := &Core{fs: fs}
	// Stream tasks observe their cloud records eagerly in tests. Per
	// instance: shared timing globals would race parallel runners.
	c.uploadStreamPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		if err := c.Close(context.Background()); err != nil {
			t.Logf("core close: %v", err)
		}
	})
	return c
}
