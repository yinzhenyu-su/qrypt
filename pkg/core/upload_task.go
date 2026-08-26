package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const uploadTaskPollInterval = 500 * time.Millisecond

type uploadTaskSpec struct {
	Items          []task.Item
	Concurrency    int
	ConflictPolicy string
}

func (c *Core) createUploadTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	spec, err := c.uploadSpecFromTaskRequest(ctx, req)
	if err != nil {
		return task.Task{}, err
	}
	items := spec.Items
	now := util.Now()
	first := items[0]
	taskType := req.Type
	if taskType == task.TypeUploadRemote && len(items) > 1 {
		taskType = task.TypeUploadBatch
	}
	item := task.Task{
		ID:        newUploadTaskID(),
		Type:      taskType,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.DestPath,
		Name:      path.Base(first.DestPath),
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal: int64(len(items)),
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  true,
			Dismissible: true,
		},
		Detail: map[string]any{
			"items":           uploadTaskDetailItems(items),
			"concurrency":     spec.Concurrency,
			"conflict_policy": spec.ConflictPolicy,
			"phase":           "queued",
		},
	}
	destMount, _, _ := moveMounts(first.DestPath, first.DestPath, c.fs)
	if destMount != "" {
		item.Mount = destMount
		item.Detail["dest_mount"] = destMount
	}
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runUploadTask(runCtx, update, spec)
	}), nil
}

func (c *Core) runUploadTask(ctx context.Context, update task.UpdateFunc, spec uploadTaskSpec) error {
	uploadService, err := c.UploadService()
	if err != nil {
		return err
	}
	items := spec.Items
	results := make([]task.ItemResult, len(items))
	var succeeded int64
	var bytesDone int64
	var done int64
	var mu sync.Mutex
	active := map[int]string{}
	jobs := make(chan int)
	workers := taskConcurrency(spec.Concurrency)
	if workers > len(items) {
		workers = len(items)
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
				item := items[i]
				mu.Lock()
				active[i] = item.DestPath
				activePaths := taskActivePaths(active)
				mu.Unlock()
				update(func(taskItem *task.Task) {
					taskItem.Progress.CurrentPath = item.DestPath
					taskItem.Progress.Phase = "stage"
					taskItem.Detail["phase"] = "stage"
					taskItem.Detail["current_source_path"] = item.SourcePath
					taskItem.Detail["active_paths"] = activePaths
				})
				uploadResult, err := uploadService.UploadLocalFileResult(ctx, UploadLocalFileRequest{
					LocalPath:      item.SourcePath,
					DestPath:       item.DestPath,
					ConflictPolicy: spec.ConflictPolicy,
				})
				var remoteTask task.Task
				var hasRemoteTask bool
				if err == nil {
					update(func(taskItem *task.Task) {
						taskItem.Progress.Phase = "upload"
						taskItem.Detail["phase"] = "upload"
					})
					remoteTask, hasRemoteTask, err = c.waitUploadTaskForPath(ctx, item.DestPath)
					if err == nil {
						if finalEntry, statErr := c.fs.Stat(ctx, item.DestPath); statErr == nil {
							uploadResult.Entry = finalEntry
						}
						if hasRemoteTask {
							uploadResult.applyRemoteTask(remoteTask)
						}
					}
				}
				result := task.ItemResult{
					Path:       item.DestPath,
					SourcePath: item.SourcePath,
					DestPath:   item.DestPath,
					Mount:      item.Mount,
					State:      task.StateSucceeded,
				}
				var addBytes int64
				if err != nil {
					result.State = task.StateFailed
					result.Error = &task.Error{Message: err.Error()}
				} else {
					if uploadResult.Skipped {
						result.Phase = "skipped"
					} else if uploadResult.Instant {
						result.Phase = "instant"
					}
					result.RemoteID = uploadResult.Entry.ID
					result.CloudBytesDone = uploadResult.Entry.Size
					result.CloudBytesTotal = uploadResult.Entry.Size
					addBytes = uploadResult.Entry.Size
				}
				mu.Lock()
				delete(active, i)
				results[i] = result
				done++
				if err == nil {
					succeeded++
					bytesDone += addBytes
				}
				doneNow := done
				succeededNow := succeeded
				bytesNow := bytesDone
				activePaths = taskActivePaths(active)
				resultSnapshot := compactItemResults(results)
				// Publish under the same lock that took the counters, so
				// concurrent workers emit monotonic progress (update order
				// == counter order; a stale snapshot can never win).
				update(func(taskItem *task.Task) {
					taskItem.Progress.ItemsDone = doneNow
					taskItem.Progress.ItemsFailed = doneNow - succeededNow
					taskItem.Progress.CloudBytesDone = bytesNow
					taskItem.Progress.CloudBytesTotal = bytesNow
					taskItem.Result.Items = resultSnapshot
					taskItem.Detail["active_paths"] = activePaths
				})
				mu.Unlock()
			}
		}()
	}
	for i := range items {
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
	failedCount := len(items) - int(succeeded)
	if failedCount == 0 {
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Detail["phase"] = "complete"
			taskItem.Detail["active_paths"] = []string{}
		})
		return nil
	}
	message := fmt.Sprintf("upload failed for %d of %d items", failedCount, len(items))
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

func (c *Core) waitUploadTaskForPath(ctx context.Context, remotePath string) (task.Task, bool, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, false, nil
	}
	source := c.fs.TaskSource()
	ticker := time.NewTicker(uploadTaskPollInterval)
	defer ticker.Stop()
	for {
		tasks, err := source.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeUploadRemote}, Path: remotePath})
		if err != nil {
			return task.Task{}, false, err
		}
		if len(tasks) == 0 {
			return task.Task{}, false, nil
		}
		item := tasks[0]
		switch item.State {
		case task.StateSucceeded:
			if dismissible, ok := source.(taskDismisser); ok {
				_ = dismissible.DismissTask(ctx, item.ID)
			}
			return item, true, nil
		case task.StateFailed, task.StateCanceled:
			if item.Error != nil {
				return item, true, fmt.Errorf("%s", item.Error.Message)
			}
			return item, true, fmt.Errorf("upload %s ended with state %s", remotePath, item.State)
		}
		select {
		case <-ctx.Done():
			return task.Task{}, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Core) uploadSpecFromTaskRequest(ctx context.Context, req task.Request) (uploadTaskSpec, error) {
	if len(req.Items) == 0 {
		return uploadTaskSpec{}, fmt.Errorf("core: upload task requires at least one item")
	}
	spec := uploadTaskSpec{Concurrency: taskConcurrency(req.Options.Concurrency)}
	switch normalizeUploadConflictPolicy(req.Options.ConflictPolicy) {
	case "", "overwrite", "replace", "fail", "error", "skip":
		spec.ConflictPolicy = normalizeUploadConflictPolicy(req.Options.ConflictPolicy)
	default:
		return uploadTaskSpec{}, fmt.Errorf("core: unsupported upload conflict_policy %q", req.Options.ConflictPolicy)
	}
	items := make([]task.Item, 0, len(req.Items))
	for _, item := range req.Items {
		sourcePath := item.SourcePath
		if sourcePath == "" {
			return uploadTaskSpec{}, fmt.Errorf("core: upload task item requires source_path")
		}
		destPath := item.DestPath
		if destPath == "" {
			destPath = item.Path
		}
		resolvedDestPath, err := c.resolveUploadDestPath(ctx, destPath, filepath.Base(sourcePath))
		if err != nil {
			return uploadTaskSpec{}, err
		}
		destPath = resolvedDestPath
		if destPath == "/" {
			return uploadTaskSpec{}, fmt.Errorf("core: upload task destination must include a file name")
		}
		destMount, _, _ := moveMounts(destPath, destPath, c.fs)
		items = append(items, task.Item{SourcePath: sourcePath, DestPath: destPath, Mount: destMount})
	}
	spec.Items = items
	return spec, nil
}

func uploadTaskDetailItems(items []task.Item) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{
			"source_path": item.SourcePath,
			"dest_path":   item.DestPath,
			"mount":       item.Mount,
		})
	}
	return out
}

func newUploadTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "upload-" + strconv.FormatInt(util.Now().UnixNano(), 36)
	}
	return "upload-" + hex.EncodeToString(b[:])
}
