package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"sync"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type copyTaskSpec struct {
	Items                 []task.Item
	OriginalItems         []task.Item
	RemoteDirs            []string
	SourceDirs            []string
	Overwrite             bool
	Recursive             bool
	DeleteSourceAfterCopy bool
	ConflictPolicy        string
	Concurrency           int
	expanded              bool
}

func (c *Core) createCopyTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	spec, err := copySpecFromTaskRequest(req)
	if err != nil {
		return task.Task{}, err
	}
	spec, err = c.expandCopyTask(ctx, spec)
	if err != nil {
		return task.Task{}, err
	}
	now := timeutil.Now()
	first := spec.OriginalItems[0]
	if len(spec.Items) > 0 {
		first = spec.Items[0]
	}
	_, _, crossQryptMount := moveMounts(first.SourcePath, first.DestPath, c.fs)
	persistent := len(spec.Items) > 1 || spec.Recursive || crossQryptMount
	item := task.Task{
		ID:        newCopyTaskID(),
		Type:      task.TypeCopy,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.SourcePath,
		Name:      path.Base(first.SourcePath),
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal: int64(len(spec.Items)),
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  persistent,
			Dismissible: persistent,
		},
		Detail: map[string]any{
			"items":                    copyTaskDetailItems(spec.Items),
			"overwrite":                spec.Overwrite,
			"recursive":                spec.Recursive,
			"delete_source_after_copy": spec.DeleteSourceAfterCopy,
			"phase":                    "queued",
			"concurrency":              spec.Concurrency,
		},
	}
	if spec.ConflictPolicy != "" {
		item.Detail["conflict_policy"] = spec.ConflictPolicy
	}
	sourceMount, destMount, _ := moveMounts(first.SourcePath, first.DestPath, c.fs)
	if sourceMount != "" {
		item.Mount = sourceMount
		item.Detail["source_mount"] = sourceMount
	}
	if destMount != "" {
		item.Detail["dest_mount"] = destMount
	}

	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runCopyTask(runCtx, update, spec)
	}), nil
}

func (c *Core) runCopyTask(ctx context.Context, update task.UpdateFunc, spec copyTaskSpec) error {
	for _, dir := range spec.RemoteDirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = dir
			taskItem.Progress.Phase = "mkdir"
			taskItem.Detail["phase"] = "mkdir"
		})
		if _, err := c.Mkdir(ctx, dir); err != nil {
			return err
		}
	}
	results := make([]task.ItemResult, len(spec.Items))
	var succeeded int64
	var bytesDone int64
	var done int64
	var mu sync.Mutex
	active := map[int]string{}
	jobs := make(chan int)
	workers := taskConcurrency(spec.Concurrency)
	if workers > len(spec.Items) {
		workers = len(spec.Items)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				item := spec.Items[i]
				mu.Lock()
				active[i] = item.SourcePath
				activePaths := taskActivePaths(active)
				mu.Unlock()
				update(func(taskItem *task.Task) {
					taskItem.Progress.CurrentPath = item.SourcePath
					taskItem.Progress.Phase = "copy"
					taskItem.Detail["phase"] = "copy"
					taskItem.Detail["current_dest_path"] = item.DestPath
					taskItem.Detail["active_paths"] = activePaths
				})
				result, err := c.copyOne(ctx, item, spec)
				resultItem := task.ItemResult{
					Path:               item.SourcePath,
					SourcePath:         item.SourcePath,
					DestPath:           item.DestPath,
					State:              task.StateSucceeded,
					RemoteID:           result.remoteID,
					TransferBytesDone:  result.bytes,
					TransferBytesTotal: result.bytes,
				}
				if err != nil {
					resultItem.State = task.StateFailed
					resultItem.Error = &task.Error{Message: err.Error()}
				}
				mu.Lock()
				delete(active, i)
				results[i] = resultItem
				done++
				if result.bytes > 0 {
					bytesDone += result.bytes
				}
				if err == nil {
					succeeded++
				}
				doneNow := done
				succeededNow := succeeded
				bytesNow := bytesDone
				activePaths = taskActivePaths(active)
				resultSnapshot := compactItemResults(results)
				mu.Unlock()
				update(func(taskItem *task.Task) {
					taskItem.Progress.ItemsDone = doneNow
					taskItem.Progress.ItemsFailed = doneNow - succeededNow
					taskItem.Progress.TransferBytesDone = bytesNow
					taskItem.Progress.TransferBytesTotal = bytesNow
					taskItem.Result.Items = resultSnapshot
					taskItem.Detail["last_copy_op_id"] = result.opID
					taskItem.Detail["active_paths"] = activePaths
				})
			}
		}()
	}
	for i := range spec.Items {
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
	failedCount := len(results) - int(succeeded)
	if failedCount == 0 {
		if spec.DeleteSourceAfterCopy {
			if err := c.removeCopiedSourceDirs(ctx, spec.SourceDirs, update); err != nil {
				return err
			}
		}
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Detail["phase"] = "complete"
			taskItem.Detail["active_paths"] = []string{}
		})
		return nil
	}
	message := fmt.Sprintf("copy failed for %d of %d items", failedCount, len(spec.Items))
	update(func(taskItem *task.Task) {
		taskItem.Progress.CurrentPath = ""
		taskItem.Detail["active_paths"] = []string{}
		taskItem.Error = &task.Error{Message: message, Retryable: true}
		taskItem.Capabilities.Retryable = true
		if succeeded > 0 {
			taskItem.State = task.StatePartialFailed
			taskItem.Progress.Phase = "partial_failed"
			taskItem.Detail["phase"] = "partial_failed"
		} else {
			taskItem.Progress.Phase = "failed"
			taskItem.Detail["phase"] = "failed"
		}
	})
	if succeeded > 0 {
		return nil
	}
	return fmt.Errorf("%s", message)
}

type copyOneResult struct {
	opID     string
	bytes    int64
	remoteID string
}

func (c *Core) copyOne(ctx context.Context, item task.Item, spec copyTaskSpec) (copyOneResult, error) {
	entry, err := c.fs.Stat(ctx, item.SourcePath)
	if err != nil {
		return copyOneResult{}, err
	}
	if entry.IsDir && !spec.Recursive {
		return copyOneResult{}, fmt.Errorf("core: source %q is a directory; recursive copy is required", item.SourcePath)
	}
	source, ok := c.fs.(control.DriverCopySource)
	if !ok {
		return copyOneResult{}, fmt.Errorf("core: copy task requires driver copy support")
	}
	if entry.IsDir {
		if spec.expanded {
			return copyOneResult{}, fmt.Errorf("core: expanded copy item %q is unexpectedly a directory", item.SourcePath)
		}
		result := control.RunDirectDriverCopyDirToPath(ctx, c.fs, source, item.SourcePath, item.DestPath, spec.Overwrite)
		out := copyOneResult{opID: result.OpID, bytes: result.Bytes}
		if !result.Pass {
			if result.Error != "" {
				return out, fmt.Errorf("%s", result.Error)
			}
			return out, fmt.Errorf("core: directory copy failed")
		}
		if spec.DeleteSourceAfterCopy {
			if err := removeMoveSource(ctx, source, item.SourcePath, true); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	result := control.RunDirectDriverCopy(ctx, source, item.SourcePath, item.DestPath, spec.Overwrite)
	out := copyOneResult{opID: result.OpID, bytes: result.Bytes}
	if result.DestEntry != nil {
		out.remoteID = result.DestEntry.ID
	}
	if !result.Pass {
		return out, fmt.Errorf("%s", control.DriverCopyError(result))
	}
	if spec.DeleteSourceAfterCopy {
		if err := removeMoveSource(ctx, source, item.SourcePath, false); err != nil {
			return out, err
		}
	}
	return out, nil
}

func copySpecFromTaskRequest(req task.Request) (copyTaskSpec, error) {
	if len(req.Items) == 0 {
		return copyTaskSpec{}, fmt.Errorf("core: copy task requires at least one item")
	}
	spec := copyTaskSpec{
		Overwrite:             req.Options.Overwrite,
		Recursive:             req.Options.Recursive,
		DeleteSourceAfterCopy: req.Options.DeleteSourceAfterCopy,
		ConflictPolicy:        req.Options.ConflictPolicy,
		Concurrency:           taskConcurrency(req.Options.Concurrency),
	}
	switch req.Options.ConflictPolicy {
	case "", "error":
	case "overwrite":
		spec.Overwrite = true
	case "skip":
		spec.Overwrite = false
	default:
		return copyTaskSpec{}, fmt.Errorf("core: unsupported copy conflict_policy %q", req.Options.ConflictPolicy)
	}
	for _, item := range req.Items {
		sourcePath := vfs.CleanVirtualPath(item.SourcePath)
		destPath := vfs.CleanVirtualPath(item.DestPath)
		if sourcePath == "/" {
			return copyTaskSpec{}, fmt.Errorf("core: copy source cannot be root")
		}
		if destPath == "/" {
			return copyTaskSpec{}, fmt.Errorf("core: copy destination must include a file or directory name")
		}
		if sourcePath == destPath {
			return copyTaskSpec{}, fmt.Errorf("core: copy source and destination are the same")
		}
		item := task.Item{SourcePath: sourcePath, DestPath: destPath}
		spec.Items = append(spec.Items, item)
		spec.OriginalItems = append(spec.OriginalItems, item)
	}
	return spec, nil
}

func (c *Core) expandCopyTask(ctx context.Context, spec copyTaskSpec) (copyTaskSpec, error) {
	if !spec.Recursive {
		return spec, nil
	}
	rawItems := spec.Items
	spec.Items = nil
	for _, item := range rawItems {
		tree, err := walkTaskTree(ctx, c.fs, item.SourcePath)
		if err != nil {
			spec.Items = append(spec.Items, item)
			continue
		}
		if !tree.Root.IsDir {
			spec.Items = append(spec.Items, item)
			continue
		}
		spec.expanded = true
		for _, dir := range tree.Dirs {
			spec.RemoteDirs = append(spec.RemoteDirs, path.Join(item.DestPath, relWithoutDot(dir.Rel)))
			if spec.DeleteSourceAfterCopy {
				spec.SourceDirs = append(spec.SourceDirs, dir.Path)
			}
		}
		for _, file := range tree.Files {
			spec.Items = append(spec.Items, task.Item{
				SourcePath: file.Path,
				DestPath:   path.Join(item.DestPath, file.Rel),
			})
		}
	}
	return spec, nil
}

func (c *Core) removeCopiedSourceDirs(ctx context.Context, dirs []string, update task.UpdateFunc) error {
	source, _ := c.fs.(control.DriverCopySource)
	for _, dir := range taskTreeDirsDeepestFirst(pathsToTaskTreeDirs(dirs)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = dir.Path
			taskItem.Progress.Phase = "delete_source"
			taskItem.Detail["phase"] = "delete_source"
		})
		if source != nil {
			if err := removeMoveSource(ctx, source, dir.Path, true); err != nil {
				return err
			}
			continue
		}
		if err := c.Remove(ctx, dir.Path); err != nil {
			return err
		}
	}
	return nil
}

func pathsToTaskTreeDirs(paths []string) []taskTreeDir {
	out := make([]taskTreeDir, 0, len(paths))
	for _, p := range paths {
		out = append(out, taskTreeDir{Path: p, Rel: p})
	}
	return out
}

func copyTaskDetailItems(items []task.Item) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{
			"source_path": item.SourcePath,
			"dest_path":   item.DestPath,
		})
	}
	return out
}

func newCopyTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "copy-" + strconv.FormatInt(timeutil.Now().UnixNano(), 36)
	}
	return "copy-" + hex.EncodeToString(b[:])
}
