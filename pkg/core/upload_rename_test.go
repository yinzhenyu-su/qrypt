package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func renameTestCore(t *testing.T) (*Core, string, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestVFS(t, fs) })
	fs.Start(ctx)
	c := newTestCore(t, fs)
	// Match Core.Open, which initializes the task manager before publication.
	if _, err := c.taskManager(); err != nil {
		t.Fatal(err)
	}
	return c, remote, ctx
}

func TestUploadRenamePreservesOriginalAndReportsFinalPath(t *testing.T) {
	t.Parallel()
	c, remote, ctx := renameTestCore(t)
	for _, name := range []string{"photo.jpg", "photo (1).jpg"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "one", DestPath: "/photo.jpg", Size: 3}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	if item.Path != "/photo (2).jpg" || item.Name != "photo (2).jpg" {
		t.Fatalf("destination: %+v", item)
	}
	h, err := c.OpenUploadStreamItem(ctx, item.ID, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Write(ctx, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err = h.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, item.ID)
	if done.State != task.StateSucceeded || done.Tracking.Items[0].DestPath != "/photo (2).jpg" {
		t.Fatalf("result %+v", done)
	}
	for name, want := range map[string]string{"photo.jpg": "original", "photo (1).jpg": "original", "photo (2).jpg": "new"} {
		got, err := os.ReadFile(filepath.Join(remote, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
}

func TestUploadRenameConcurrentReservations(t *testing.T) {
	t.Parallel()
	c, _, ctx := renameTestCore(t)
	const count = 12
	var wg sync.WaitGroup
	paths := make(chan string, count)
	for i := range count {
		wg.Go(func() {
			item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamBatch, Items: []task.Item{{ItemID: fmt.Sprint(i), DestPath: "/same.txt", Size: 1}}, Options: task.Options{ConflictPolicy: "rename"}})
			if err != nil {
				t.Error(err)
				return
			}
			paths <- item.Path
		})
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for p := range paths {
		if seen[p] {
			t.Fatalf("duplicate reservation %s", p)
		}
		seen[p] = true
	}
	if len(seen) != count {
		t.Fatalf("paths=%v", seen)
	}
}

func TestUploadRenameBatchAndHiddenFile(t *testing.T) {
	t.Parallel()
	c, remote, ctx := renameTestCore(t)
	if err := os.WriteFile(filepath.Join(remote, ".env"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamBatch, Items: []task.Item{{ItemID: "a", DestPath: "/.env", Size: 1}, {ItemID: "b", DestPath: "/.env", Size: 1}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	if item.Tracking.Items[0].DestPath != "/.env (1)" || item.Tracking.Items[1].DestPath != "/.env (2)" {
		t.Fatalf("items %+v", item.Tracking.Items)
	}
}

func TestUploadRenameLocalTaskUsesFinalPath(t *testing.T) {
	t.Parallel()
	c, remote, ctx := renameTestCore(t)
	if err := os.WriteFile(filepath.Join(remote, "note.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadRemote, Items: []task.Item{{SourcePath: source, DestPath: "/note.txt"}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, item.ID)
	if done.State != task.StateSucceeded || done.Tracking.Items[0].DestPath != "/note (1).txt" {
		t.Fatalf("task %+v", done)
	}
	got, err := os.ReadFile(filepath.Join(remote, "note.txt"))
	if err != nil || string(got) != "old" {
		t.Fatalf("original=%q err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(remote, "note (1).txt"))
	if err != nil || string(got) != "new" {
		t.Fatalf("renamed=%q err=%v", got, err)
	}
}

func TestUploadRenameDirectPreservesOriginal(t *testing.T) {
	t.Parallel()
	c, remote, ctx := renameTestCore(t)
	if err := os.WriteFile(filepath.Join(remote, "note.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamDirect, Items: []task.Item{{ItemID: "one", SourcePath: source, DestPath: "/note.txt", Size: 3}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, item.ID)
	if done.State != task.StateSucceeded || done.Tracking.Items[0].DestPath != "/note (1).txt" {
		t.Fatalf("task %+v", done)
	}
	for name, want := range map[string]string{"note.txt": "old", "note (1).txt": "new"} {
		got, err := os.ReadFile(filepath.Join(remote, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
}

func TestUploadRenameValidationReleasesReservations(t *testing.T) {
	t.Parallel()
	c, _, ctx := renameTestCore(t)
	_, err := c.uploadStreamBatchFromRequest(ctx, task.Request{Items: []task.Item{{ItemID: "a", DestPath: "/file.txt", Size: 1}, {ItemID: "b", DestPath: "/other.txt", Size: -1}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err == nil {
		t.Fatal("invalid size accepted")
	}
	c.uploadDestinations.mu.Lock()
	defer c.uploadDestinations.mu.Unlock()
	if len(c.uploadDestinations.paths) != 0 {
		t.Fatalf("leaked reservations: %v", c.uploadDestinations.paths)
	}
}

func TestUploadRenameRecoveredDestinationStaysReserved(t *testing.T) {
	t.Parallel()
	c, _, ctx := renameTestCore(t)
	original, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamBatch, Items: []task.Item{{ItemID: "a", DestPath: "/pending.txt", Size: 1}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	recoveredCore := newTestCore(t, c.fs)
	recovered, err := recoveredCore.uploadStreamBatchFromTask(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	recoveredCore.putUploadStream(recovered)
	defer recoveredCore.removeUploadStream(recovered.taskID)
	service, err := recoveredCore.UploadService()
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, release, err := service.prepareDestination(ctx, "/pending.txt", "rename")
	defer release()
	if err != nil || next != "/pending (1).txt" {
		t.Fatalf("next=%q err=%v", next, err)
	}
}

func TestUploadRenameIdempotentSubmissionKeepsDestination(t *testing.T) {
	t.Parallel()
	c, _, ctx := renameTestCore(t)
	req := task.OperationRequest{Operation: task.OperationUpload, UploadPolicy: task.UploadPolicyStagingOnly, UploadSource: task.UploadSourceApp,
		Items: []task.Item{{ItemID: "one", DestPath: "/once.txt", Size: 1}}, Options: task.Options{ConflictPolicy: "rename"}, IdempotencyKey: "rename-once"}
	first, err := c.CreateOperation(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateOperation(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Path != second.Path {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	c.uploadDestinations.mu.Lock()
	defer c.uploadDestinations.mu.Unlock()
	if len(c.uploadDestinations.paths) != 1 || c.uploadDestinations.paths[first.Path] != 1 {
		t.Fatalf("reservations=%v", c.uploadDestinations.paths)
	}
}

func TestUploadRenameReservationCannotBeReplacedByNonOverwritePolicy(t *testing.T) {
	t.Parallel()
	c, _, ctx := renameTestCore(t)
	service, err := c.UploadService()
	if err != nil {
		t.Fatal(err)
	}
	dest, _, _, release, err := service.prepareDestination(ctx, "/pending.txt", "rename")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, policy := range []string{"fail", "error", "skip"} {
		_, _, _, done, err := service.prepareDestination(ctx, dest, policy)
		done()
		if err == nil {
			t.Fatalf("%s accepted a reserved name", policy)
		}
	}
}
