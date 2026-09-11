package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCreateTaskSameMountMoveRenamesPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("same mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), RootID: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, moveTaskRequest("/old.txt", "/new.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeMoveRemote || item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded move_remote", item)
	}
	if _, err := os.Stat(filepath.Join(remote, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old path still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "new.txt")); err != nil || string(data) != "same mount" {
		t.Fatalf("new data = %q err=%v", data, err)
	}
	tasks, err := c.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeMoveRemote}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != item.ID {
		t.Fatalf("tasks = %+v, want created move task", tasks)
	}
}

func TestCreateTaskMoveRemoteRenamesPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("create task move"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), RootID: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, moveTaskRequest("/old.txt", "/new.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeMoveRemote || item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded move_remote", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "new.txt")); err != nil || string(data) != "create task move" {
		t.Fatalf("new data = %q err=%v", data, err)
	}
}

func TestCreateTaskMoveRemoteBatchesSameMountItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), RootID: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	req := task.Request{Type: task.TypeMoveRemote, Items: []task.Item{
		{SourcePath: "/one.txt", DestPath: "/moved-one.txt"},
		{SourcePath: "/two.txt", DestPath: "/moved-two.txt"},
	}}
	item, err := c.CreateTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || len(item.Result.Items) != 2 {
		t.Fatalf("task = %+v, want two-item move success", item)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(remote, name)); !os.IsNotExist(err) {
			t.Fatalf("source %s still exists, stat err=%v", name, err)
		}
	}
}

func TestCreateTaskMoveRemoteBatchReportsPartialFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "present.txt"), []byte("present"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), RootID: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeMoveRemote, Items: []task.Item{
		{SourcePath: "/present.txt", DestPath: "/moved.txt"},
		{SourcePath: "/missing.txt", DestPath: "/missing-moved.txt"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || !item.Capabilities.Retryable || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 1 {
		t.Fatalf("task = %+v, want retryable partial failure", item)
	}
	if len(item.Result.Items) != 2 || item.Result.Items[1].Error == nil {
		t.Fatalf("result items = %+v, want failed item detail", item.Result.Items)
	}
	if _, err := os.Stat(filepath.Join(remote, "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("missing source unexpectedly changed, stat err=%v", err)
	}
}

func TestCreateTaskCrossQryptMountCopiesThenDeletesSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("cross mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/file.txt", "/dst/file.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Detail["mode"] != "copy_delete" {
		t.Fatalf("task = %+v, want copy_delete success", item)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "file.txt")); err != nil || string(data) != "cross mount" {
		t.Fatalf("dest data = %q err=%v", data, err)
	}
}

func TestCreateTaskCrossQryptMountRejectsDirectoryWithoutRecursive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.Mkdir(filepath.Join(srcRemote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/dir", "/dst/dir", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || item.Error == nil || item.Error.Message == "" {
		t.Fatalf("task = %+v, want failed task", item)
	}
}

func TestCreateTaskCrossQryptMountBatchesItemsAndDeletesSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(srcRemote, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeMoveRemote, Items: []task.Item{
		{SourcePath: "/src/one.txt", DestPath: "/dst/one.txt"},
		{SourcePath: "/src/two.txt", DestPath: "/dst/two.txt"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || len(item.Result.Items) != 2 {
		t.Fatalf("task = %+v, want cross-mount batch success", item)
	}
	if item.Progress.TransferBytesDone <= 0 || item.Progress.TransferBytesTotal <= 0 {
		t.Fatalf("task = %+v, want copied byte progress", item)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(srcRemote, name)); !os.IsNotExist(err) {
			t.Fatalf("source %s still exists, stat err=%v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dstRemote, name)); err != nil {
			t.Fatalf("destination %s missing: %v", name, err)
		}
	}
}

func TestCreateTaskCrossQryptMountPreservesSourceWhenDeleteFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("preserve me"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(&moveDeleteFailDriver{Driver: localfs.New(srcRemote)}, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/file.txt", "/dst/file.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || item.Error == nil {
		t.Fatalf("task = %+v, want failed cleanup", item)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "file.txt")); err != nil {
		t.Fatalf("source was not preserved: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "file.txt")); err != nil || string(data) != "preserve me" {
		t.Fatalf("destination = %q err=%v, want copied data", data, err)
	}
}

type moveDeleteFailDriver struct {
	*localfs.Driver
}

func (d *moveDeleteFailDriver) Remove(context.Context, drive.Entry) error {
	return errors.New("move test: source delete failed")
}

func TestCreateTaskCrossQryptMountMovesDirectoryToRenamedDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRemote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "root.txt"), []byte("root move"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "nested", "file.txt"), []byte("move dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/dir", "/dst/renamed", false, true))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Detail["mode"] != "copy_delete" || item.Progress.ItemsDone != 2 || item.Progress.ItemsTotal != 2 {
		t.Fatalf("task = %+v, want renamed directory move success", item)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "dir")); !os.IsNotExist(err) {
		t.Fatalf("source dir still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "nested", "file.txt")); err != nil || string(data) != "move dir" {
		t.Fatalf("moved nested data = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "root.txt")); err != nil || string(data) != "root move" {
		t.Fatalf("moved root data = %q err=%v", data, err)
	}
}

// TestCreateTaskCrossQryptMountBatchKeepsSourceWhenDestinationExists: a batch
// directory move with overwrite disabled must not remove a source whose
// destination files were skipped - the directory copy reports those as
// skipped, not failed, so the source would otherwise be deleted while the
// destination keeps its old content.
func TestCreateTaskCrossQryptMountBatchKeepsSourceWhenDestinationExists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRemote, "d1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "d1", "a.txt"), []byte("new-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "plain.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstRemote, "d1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRemote, "d1", "a.txt"), []byte("old-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeMoveRemote, Items: []task.Item{
		{SourcePath: "/src/d1", DestPath: "/dst/d1"},
		{SourcePath: "/src/plain.txt", DestPath: "/dst/plain.txt"},
	}, Options: task.Options{Recursive: true}})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || item.Progress.ItemsFailed != 1 {
		t.Fatalf("task = %+v, want the skipped directory item to fail", item)
	}
	if len(item.Result.Items) != 2 || item.Result.Items[0].State != task.StateFailed || item.Result.Items[0].Error == nil {
		t.Fatalf("result items = %+v, want a failed directory item", item.Result.Items)
	}
	if data, err := os.ReadFile(filepath.Join(srcRemote, "d1", "a.txt")); err != nil || string(data) != "new-a" {
		t.Fatalf("source d1/a.txt = %q err=%v, want it preserved", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "d1", "a.txt")); err != nil || string(data) != "old-a" {
		t.Fatalf("destination d1/a.txt = %q err=%v, want the existing file untouched", data, err)
	}
	if _, err := os.Stat(filepath.Join(dstRemote, "plain.txt")); err != nil {
		t.Fatalf("independent item did not move: %v", err)
	}
}

// TestCreateTaskMoveBatchRetryOnlyRunsUnfinishedItems: retrying a partially
// failed batch must not re-run the items an earlier attempt already moved,
// whose sources no longer exist.
func TestCreateTaskMoveBatchRetryOnlyRunsUnfinishedItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	driver := &flakyRenameDriver{Driver: localfs.New(remote), fail: map[string]int{"one.txt": 1}}
	fs, err := vfs.New(driver, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), RootID: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeMoveRemote, Items: []task.Item{
		{SourcePath: "/one.txt", DestPath: "/moved-one.txt"},
		{SourcePath: "/two.txt", DestPath: "/moved-two.txt"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || !item.Capabilities.Retryable {
		t.Fatalf("task = %+v, want a retryable partial failure", item)
	}
	if err := c.RetryTask(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 0 {
		t.Fatalf("task after retry = %+v, want every item moved", item)
	}
	if counts := driver.renameCounts(); counts["one.txt"] != 2 || counts["two.txt"] != 1 {
		t.Fatalf("rename attempts = %v, want one.txt retried once and two.txt untouched", counts)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(remote, name)); !os.IsNotExist(err) {
			t.Fatalf("source %s still exists, stat err=%v", name, err)
		}
	}
}

// flakyRenameDriver fails the configured number of leading rename attempts per
// entry name so retry behaviour is observable without a real transient fault.
type flakyRenameDriver struct {
	*localfs.Driver
	mu     sync.Mutex
	fail   map[string]int
	counts map[string]int
}

func (d *flakyRenameDriver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	d.mu.Lock()
	if d.counts == nil {
		d.counts = map[string]int{}
	}
	d.counts[entry.Name]++
	fail := d.fail[entry.Name] > 0
	if fail {
		d.fail[entry.Name]--
	}
	d.mu.Unlock()
	if fail {
		return errors.New("move test: transient rename failure")
	}
	return d.Driver.Rename(ctx, entry, newName)
}

func (d *flakyRenameDriver) renameCounts() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]int, len(d.counts))
	for name, count := range d.counts {
		out[name] = count
	}
	return out
}

func moveTaskRequest(sourcePath, destPath string, overwrite, recursive bool) task.Request {
	return task.Request{
		Type: task.TypeMoveRemote,
		Items: []task.Item{{
			SourcePath: sourcePath,
			DestPath:   destPath,
		}},
		Options: task.Options{
			Overwrite: overwrite,
			Recursive: recursive,
		},
	}
}

func waitCoreTask(t *testing.T, c *Core, id string) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := c.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch item.State {
		case task.StateSucceeded, task.StatePartialFailed, task.StateFailed, task.StateCanceled:
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := c.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task %s did not finish, last state=%s", id, item.State)
	return task.Task{}
}
