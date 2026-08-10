package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"sync"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type deleteTaskSpec struct {
	Items       []task.Item
	Recursive   bool
	Concurrency int
	DirStart    int
}

func (c *Core) createDeleteTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	spec, err := deleteSpecFromTaskRequest(req)
	if err != nil {
		return task.Task{}, err
	}
	spec, err = c.expandDeleteTask(ctx, spec)
	if err != nil {
		return task.Task{}, err
	}
	items := spec.Items
	now := timeutil.Now()
	firstPath := items[0].Path
	taskType := req.Type
	if taskType == task.TypeDeleteRemote && len(items) > 1 {
		taskType = task.TypeDeleteBatch
	}
	item := task.Task{
		ID:        newDeleteTaskID(),
		Type:      taskType,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      firstPath,
		Name:      path.Base(firstPath),
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal: int64(len(items)),
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  taskType == task.TypeDeleteBatch,
			Dismissible: taskType == task.TypeDeleteBatch,
		},
		Detail: map[string]any{
			"paths":       deleteTaskPaths(items),
			"recursive":   spec.Recursive,
			"concurrency": spec.Concurrency,
			"phase":       "queued",
		},
	}

	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runDeleteTask(runCtx, update, spec)
	}), nil
}

func (c *Core) runDeleteTask(ctx context.Context, update task.UpdateFunc, spec deleteTaskSpec) error {
	items := spec.Items
	results := make([]task.ItemResult, len(items))
	var succeeded int64
	var done int64
	var mu sync.Mutex
	active := map[int]string{}
	deleteOne := func(i int) {
		item := items[i]
		current := item.Path
		mu.Lock()
		active[i] = current
		activePaths := taskActivePaths(active)
		mu.Unlock()
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = current
			taskItem.Progress.Phase = "delete"
			taskItem.Detail["active_paths"] = activePaths
		})
		result := task.ItemResult{Path: current, State: task.StateSucceeded}
		if err := c.Remove(ctx, current); err != nil {
			result.State = task.StateFailed
			result.Error = &task.Error{Message: err.Error()}
		}
		mu.Lock()
		delete(active, i)
		results[i] = result
		done++
		if result.State == task.StateSucceeded {
			succeeded++
		}
		doneNow := done
		succeededNow := succeeded
		activePaths = taskActivePaths(active)
		resultSnapshot := compactItemResults(results)
		// The progress update runs UNDER the same lock that took the
		// counters, so concurrent workers publish monotonic progress:
		// a stale (lower) counter snapshot can never overwrite a newer
		// one (update order == counter order).
		update(func(taskItem *task.Task) {
			taskItem.Progress.ItemsDone = doneNow
			taskItem.Progress.ItemsFailed = doneNow - succeededNow
			taskItem.Result.Items = resultSnapshot
			taskItem.Detail["active_paths"] = activePaths
		})
		mu.Unlock()
	}
	fileEnd := spec.DirStart
	if fileEnd <= 0 || fileEnd > len(items) {
		fileEnd = len(items)
	}
	jobs := make(chan int)
	workers := taskConcurrency(spec.Concurrency)
	if workers > fileEnd {
		workers = fileEnd
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
				deleteOne(i)
			}
		}()
	}
	for i := 0; i < fileEnd; i++ {
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
	for i := fileEnd; i < len(items); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleteOne(i)
	}
	failedCount := len(results) - int(succeeded)
	if failedCount == 0 {
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Detail["active_paths"] = []string{}
		})
		return nil
	}
	message := fmt.Sprintf("delete failed for %d of %d items", failedCount, len(items))
	update(func(taskItem *task.Task) {
		taskItem.Progress.CurrentPath = ""
		taskItem.Detail["active_paths"] = []string{}
		taskItem.Error = &task.Error{Message: message, Retryable: true}
		if succeeded > 0 {
			taskItem.State = task.StatePartialFailed
			taskItem.Progress.Phase = "partial_failed"
			taskItem.Capabilities.Retryable = true
		} else {
			taskItem.Progress.Phase = "failed"
		}
	})
	if succeeded > 0 {
		return nil
	}
	return fmt.Errorf("%s", message)
}

func cloneItemResults(results []task.ItemResult) []task.ItemResult {
	out := make([]task.ItemResult, len(results))
	for i, result := range results {
		out[i] = result
		if result.Error != nil {
			err := *result.Error
			out[i].Error = &err
		}
	}
	return out
}

func compactItemResults(results []task.ItemResult) []task.ItemResult {
	out := make([]task.ItemResult, 0, len(results))
	for _, result := range results {
		if result.State == "" {
			continue
		}
		out = append(out, result)
	}
	return cloneItemResults(out)
}

func deleteSpecFromTaskRequest(req task.Request) (deleteTaskSpec, error) {
	if len(req.Items) == 0 {
		return deleteTaskSpec{}, fmt.Errorf("core: delete task requires at least one item")
	}
	spec := deleteTaskSpec{Recursive: req.Options.Recursive, Concurrency: taskConcurrency(req.Options.Concurrency)}
	items := make([]task.Item, 0, len(req.Items))
	for _, item := range req.Items {
		p := vfs.CleanVirtualPath(item.Path)
		if p == "/" {
			return deleteTaskSpec{}, fmt.Errorf("core: delete task cannot remove root")
		}
		items = append(items, task.Item{Path: p})
	}
	spec.Items = items
	return spec, nil
}

func (c *Core) expandDeleteTask(ctx context.Context, spec deleteTaskSpec) (deleteTaskSpec, error) {
	rawItems := spec.Items
	spec.Items = nil
	fileItems := make([]task.Item, 0, len(rawItems))
	dirItems := []task.Item{}
	for _, item := range rawItems {
		tree, err := walkTaskTree(ctx, c.fs, item.Path)
		if err != nil {
			fileItems = append(fileItems, item)
			continue
		}
		if !tree.Root.IsDir {
			fileItems = append(fileItems, item)
			continue
		}
		if !spec.Recursive {
			return deleteTaskSpec{}, fmt.Errorf("core: delete source %q is a directory; recursive delete is required", item.Path)
		}
		for _, file := range tree.Files {
			fileItems = append(fileItems, task.Item{Path: file.Path})
		}
		for _, dir := range taskTreeDirsDeepestFirst(tree.Dirs) {
			dirItems = append(dirItems, task.Item{Path: dir.Path})
		}
	}
	spec.DirStart = len(fileItems)
	spec.Items = append(fileItems, dirItems...)
	if len(spec.Items) == 0 {
		return deleteTaskSpec{}, fmt.Errorf("core: delete task has no items")
	}
	return spec, nil
}

func deleteTaskPaths(items []task.Item) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
	}
	return paths
}

func newDeleteTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "delete-" + strconv.FormatInt(timeutil.Now().UnixNano(), 36)
	}
	return "delete-" + hex.EncodeToString(b[:])
}
