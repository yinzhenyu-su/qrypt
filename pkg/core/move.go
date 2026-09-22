package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

type moveTaskSpec struct {
	Type        task.Type
	Items       []task.Item
	Overwrite   bool
	Recursive   bool
	Concurrency int
}

func (c *Core) createMoveTask(ctx context.Context, req moveTaskSpec) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	if len(req.Items) == 0 {
		return task.Task{}, fmt.Errorf("core: move task requires at least one item")
	}
	for i := range req.Items {
		req.Items[i].SourcePath = vfs.CleanVirtualPath(req.Items[i].SourcePath)
		req.Items[i].DestPath = vfs.CleanVirtualPath(req.Items[i].DestPath)
		if req.Items[i].SourcePath == "/" || req.Items[i].DestPath == "/" {
			return task.Task{}, fmt.Errorf("core: move source and destination must be files or directories")
		}
		if req.Items[i].SourcePath == req.Items[i].DestPath {
			return task.Task{}, fmt.Errorf("core: move source and destination are the same")
		}
	}
	first := req.Items[0]

	// Mirror delete tasks: an explicit batch type keeps its identity, while a
	// multi-item remote move is promoted to the batch type.
	taskType := task.Promote(req.Type, len(req.Items))
	if taskType == "" {
		taskType = task.TypeMoveRemote
	}

	now := util.Now()
	item := task.Task{
		ID:           newMoveTaskID(),
		Type:         taskType,
		State:        task.StateQueued,
		Scope:        task.ScopeForType(taskType),
		Path:         first.SourcePath,
		Name:         path.Base(first.SourcePath),
		Progress:     task.Progress{ItemsTotal: int64(len(req.Items))},
		CreatedAt:    now,
		UpdatedAt:    now,
		Capabilities: taskCreationCapabilities(taskType),
		Detail: map[string]any{
			"items":       copyTaskDetailItems(req.Items),
			"overwrite":   req.Overwrite,
			"recursive":   req.Recursive,
			"concurrency": taskConcurrency(req.Concurrency),
		},
	}

	sourceMount, destMount, crossQryptMount := moveMounts(first.SourcePath, first.DestPath, c.fs)
	if sourceMount != "" {
		item.Mount = sourceMount
		item.Detail["source_mount"] = sourceMount
	}
	if destMount != "" {
		item.Detail["dest_mount"] = destMount
	}
	var crossCopySpec copyTaskSpec
	if len(req.Items) == 1 && crossQryptMount {
		crossCopySpec = copyTaskSpec{
			Items:                 []task.Item{first},
			OriginalItems:         []task.Item{first},
			Overwrite:             req.Overwrite,
			Recursive:             req.Recursive,
			Concurrency:           taskConcurrency(req.Concurrency),
			DeleteSourceAfterCopy: true,
		}
		var err error
		crossCopySpec, err = c.expandCopyTask(ctx, crossCopySpec)
		if err != nil {
			return task.Task{}, err
		}
		item.Progress.ItemsTotal = int64(len(crossCopySpec.Items))
		item.Detail["items"] = copyTaskDetailItems(crossCopySpec.Items)
		item.Capabilities.Persistent = true
		item.Capabilities.Dismissible = true
	}

	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		runItem := item
		runItem.Detail = cloneTaskDetail(item.Detail)
		var err error
		if len(req.Items) == 1 && crossQryptMount {
			update(func(item *task.Task) {
				item.Detail["mode"] = "copy_delete"
				item.Detail["phase"] = "copy"
			})
			return c.runCopyTask(runCtx, update, crossCopySpec)
		} else if len(req.Items) == 1 {
			update(func(item *task.Task) {
				item.Detail["mode"] = "server_move"
				item.Detail["phase"] = "move"
			})
			runItem.Detail["mode"] = "server_move"
			runItem.Detail["phase"] = "move"
			err = c.fs.Rename(runCtx, first.SourcePath, first.DestPath)
			if err == nil {
				c.refreshMovePaths(first.SourcePath, first.DestPath)
			}
		} else {
			err = c.runMoveBatch(runCtx, update, req, c.movePriorItems(runCtx, item.ID))
		}
		update(func(item *task.Task) {
			if len(req.Items) == 1 {
				item.Detail = cloneTaskDetail(runItem.Detail)
			}
			if err == nil && len(req.Items) == 1 {
				item.Detail["phase"] = "complete"
			}
		})
		return err
	}), nil
}

func (c *Core) removeMoveSource(ctx context.Context, source diagnostics.DriverCopySource, sourcePath string, isDir bool) error {
	info, err := source.DebugResolve(ctx, sourcePath, false)
	if err != nil {
		return err
	}
	drivers := map[string]drive.Driver{}
	for _, named := range source.Drivers() {
		drivers[named.Name] = named.Driver
	}
	driver := drivers[info.Mount]
	if driver == nil && len(drivers) == 1 {
		for _, item := range drivers {
			driver = item
		}
	}
	if driver == nil {
		return fmt.Errorf("core: source driver %q not found", info.Mount)
	}
	if err := driver.Remove(ctx, drive.Entry{
		ID:       info.RemoteID,
		ParentID: info.ParentID,
		Name:     info.PlainName,
		IsDir:    isDir,
		Size:     info.Size,
	}); err != nil {
		return err
	}
	// The direct driver removal is synchronous, but it bypasses the VFS view
	// mutation path. Invalidate the containing directory so the next listing
	// cannot resurrect the deleted source from a stale cache.
	c.fs.RefreshPath(path.Dir(sourcePath))
	return nil
}

// movePriorItems returns the tracked items an earlier attempt recorded on a move
// task, so a retry re-runs only the items that did not succeed. A first run (or
// an unreadable task) yields none and every item runs.
func (c *Core) movePriorItems(ctx context.Context, id string) []task.ItemTracking {
	if id == "" {
		return nil
	}
	current, err := c.GetTask(ctx, id)
	if err != nil {
		return nil
	}
	return current.Tracking.Items
}

// moveBatchKey identifies one requested move across attempts.
type moveBatchKey struct {
	source string
	dest   string
}

// runMoveBatch moves every item of a multi-item move, publishing per-item
// results and monotonic progress. prior carries the item results of an earlier
// attempt so a retry never re-resolves a source an earlier attempt already
// moved away. Items run on taskConcurrency(req.Concurrency) workers.
func (c *Core) runMoveBatch(ctx context.Context, update task.UpdateFunc, req moveTaskSpec, prior []task.ItemTracking) error {
	completed := make(map[moveBatchKey]task.ItemTracking, len(prior))
	for _, result := range prior {
		if result.SourcePath != "" && result.State == task.ItemStateSucceeded {
			completed[moveBatchKey{source: result.SourcePath, dest: result.DestPath}] = result
		}
	}
	results := make([]task.ItemTracking, len(req.Items))
	var succeeded int64
	var bytesDone int64
	var done int64
	var mu sync.Mutex
	active := map[int]string{}
	// publish reports progress for the items finished so far. The caller holds
	// mu, so concurrent workers emit monotonic counters and snapshots.
	publish := func() {
		doneNow := done
		succeededNow := succeeded
		bytesNow := bytesDone
		activePaths := taskActivePaths(active)
		snapshot := compactTrackingItems(results)
		update(func(t *task.Task) {
			t.Progress.ItemsDone = doneNow
			t.Progress.ItemsFailed = doneNow - succeededNow
			t.Progress.TransferBytesDone = bytesNow
			t.Progress.TransferBytesTotal = bytesNow
			t.Tracking.Items = snapshot
			t.Detail["active_paths"] = activePaths
		})
	}
	moveOne := func(i int) {
		item := req.Items[i]
		if previous, ok := completed[moveBatchKey{source: item.SourcePath, dest: item.DestPath}]; ok {
			// An earlier attempt already moved this item; re-resolving or
			// re-copying its source would fail, because the move removed it.
			mu.Lock()
			results[i] = previous
			done++
			succeeded++
			publish()
			mu.Unlock()
			return
		}
		_, _, cross := moveMounts(item.SourcePath, item.DestPath, c.fs)
		mu.Lock()
		active[i] = item.SourcePath
		activePaths := taskActivePaths(active)
		mu.Unlock()
		update(func(t *task.Task) {
			t.Progress.CurrentPath = item.SourcePath
			t.Progress.Phase = "move"
			t.Detail["phase"] = "move"
			t.Detail["active_paths"] = activePaths
			if cross {
				t.Detail["mode"] = "copy_delete"
			} else {
				t.Detail["mode"] = "server_move"
			}
		})

		var result copyOneResult
		var err error
		if cross {
			result, err = c.copyOne(ctx, task.Item{SourcePath: item.SourcePath, DestPath: item.DestPath}, copyTaskSpec{
				Overwrite:             req.Overwrite,
				Recursive:             req.Recursive,
				DeleteSourceAfterCopy: true,
			})
		} else {
			err = c.fs.Rename(ctx, item.SourcePath, item.DestPath)
			if err == nil {
				c.refreshMovePaths(item.SourcePath, item.DestPath)
			}
		}
		itemTracking := task.ItemTracking{
			Path:               item.SourcePath,
			SourcePath:         item.SourcePath,
			DestPath:           item.DestPath,
			State:              task.ItemStateSucceeded,
			RemoteID:           result.remoteID,
			TransferBytesDone:  result.bytes,
			TransferBytesTotal: result.bytes,
		}
		if err != nil {
			itemTracking.State = task.ItemStateFailed
			itemTracking.Error = &task.Error{Message: err.Error()}
		}
		mu.Lock()
		delete(active, i)
		results[i] = itemTracking
		done++
		if err == nil {
			succeeded++
			bytesDone += result.bytes
		}
		publish()
		mu.Unlock()
	}

	workers := taskConcurrency(req.Concurrency)
	if workers > len(req.Items) {
		workers = len(req.Items)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				moveOne(i)
			}
		}()
	}
	for i := range req.Items {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	failed := int64(len(req.Items)) - succeeded
	update(func(t *task.Task) {
		t.Progress.CurrentPath = ""
		t.Detail["phase"] = "complete"
		t.Detail["active_paths"] = []string{}
		if failed == 0 {
			// Every item is in place, including the ones an earlier attempt
			// moved: stop offering a retry that would only re-scan them.
			t.Capabilities.Retryable = false
			return
		}
		t.Error = &task.Error{Message: fmt.Sprintf("move failed for %d of %d items", failed, len(req.Items)), Retryable: true}
		t.Capabilities.Retryable = true
		if succeeded > 0 {
			t.State = task.StatePartialFailed
			t.Progress.Phase = "partial_failed"
			t.Detail["phase"] = "partial_failed"
			return
		}
		t.Progress.Phase = "failed"
		t.Detail["phase"] = "failed"
	})
	if failed > 0 && succeeded == 0 {
		return fmt.Errorf("move failed for %d of %d items", failed, len(req.Items))
	}
	return nil
}

func (c *Core) refreshMovePaths(sourcePath, destPath string) {
	c.fs.RefreshPath(path.Dir(sourcePath))
	c.fs.RefreshPath(path.Dir(destPath))
}

func moveMounts(sourcePath, destPath string, fs vfs.FileSystem) (string, string, bool) {
	if _, ok := fs.(*vfs.Namespace); !ok {
		return "", "", false
	}
	sourceMount, _, sourceRoot := splitMoveNamespacePath(sourcePath)
	destMount, _, destRoot := splitMoveNamespacePath(destPath)
	if sourceRoot || destRoot {
		return sourceMount, destMount, false
	}
	return sourceMount, destMount, sourceMount != "" && destMount != "" && sourceMount != destMount
}

func cloneTaskDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	clone := make(map[string]any, len(detail))
	for key, value := range detail {
		clone[key] = value
	}
	return clone
}

func splitMoveNamespacePath(p string) (string, string, bool) {
	p = vfs.CleanVirtualPath(p)
	if p == "/" {
		return "", "/", true
	}
	trimmed := strings.TrimPrefix(p, "/")
	mount, rest, ok := strings.Cut(trimmed, "/")
	if !ok {
		return mount, "/", false
	}
	return mount, "/" + rest, false
}

func newMoveTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "move-" + strconv.FormatInt(util.Now().UnixNano(), 36)
	}
	return "move-" + hex.EncodeToString(b[:])
}
