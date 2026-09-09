package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type Task = task.Task
type TaskFilter = task.Filter
type TaskItem = task.Item
type TaskOptions = task.Options
type TaskRequest = task.Request
type TaskOperationRequest = task.OperationRequest
type TaskType = task.Type
type TaskState = task.State
type TaskItemFilter = task.ItemFilter

// taskDismisser is the optional task-dismissal capability upload tasks use
// when their source exposes it (a Core session). Named so fakes are trivial.
type taskDismisser interface {
	DismissTask(context.Context, string) error
}

func (c *Core) newTaskManager() *task.Manager {
	sources := []task.Source{c.fs.TaskSource()}
	store := c.taskStore()
	manager := task.NewManagerWithStore(store, sources...)
	c.recoverUploadStreamTasks(context.Background(), manager)
	c.recoverUploadStreamDirectTasks(context.Background(), manager)
	return manager
}

func (c *Core) taskStore() task.Store {
	if c == nil || c.runtimeLayout.StateDir == "" {
		return task.NewMemoryStore()
	}
	store, err := task.NewPersistentStore(filepath.Join(c.runtimeLayout.StateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		return task.NewMemoryStore()
	}
	return store
}

func (c *Core) taskManager() (*task.Manager, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	if c.tasks == nil {
		c.tasks = c.newTaskManager()
	}
	return c.tasks, nil
}

// TaskPersistenceError reports a degraded task journal without conflating it
// with the result of the file operation represented by a task.
func (c *Core) TaskPersistenceError() error {
	if c == nil || c.tasks == nil {
		return nil
	}
	return c.tasks.PersistenceError()
}

func (c *Core) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	manager, err := c.taskManager()
	if err != nil {
		return nil, err
	}
	return manager.ListTasks(ctx, filter)
}

func (c *Core) GetTask(ctx context.Context, id string) (task.Task, error) {
	if id == "" {
		return task.Task{}, fmt.Errorf("core: task id required")
	}
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.GetTask(ctx, id)
}

func (c *Core) ListTaskItems(ctx context.Context, taskID string, filter task.ItemFilter) ([]task.ItemResult, error) {
	if controller, err := c.itemController(taskID); err == nil {
		return controller.listItems(filter), nil
	}
	item, err := c.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]task.ItemResult, 0, len(item.Result.Items))
	for _, result := range item.Result.Items {
		if !filter.Match(result) {
			continue
		}
		out = append(out, result)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (c *Core) OpenTaskItem(ctx context.Context, taskID, itemID string, action task.Action) (TaskItemHandle, error) {
	if taskID == "" || itemID == "" {
		return nil, fmt.Errorf("core: task and item ids are required")
	}
	controller, err := c.itemController(taskID)
	if err != nil {
		return nil, err
	}
	return controller.openItem(ctx, itemID, action)
}

func (c *Core) CommitTaskItem(ctx context.Context, taskID, itemID string) error {
	controller, err := c.itemController(taskID)
	if err != nil {
		return err
	}
	return controller.commitItem(ctx, itemID)
}

func (c *Core) GetTaskItem(ctx context.Context, taskID, itemID string) (task.ItemResult, error) {
	if itemID == "" {
		return task.ItemResult{}, fmt.Errorf("core: task item id required")
	}
	items, err := c.ListTaskItems(ctx, taskID, task.ItemFilter{ItemID: itemID, Limit: 1})
	if err != nil {
		return task.ItemResult{}, err
	}
	if len(items) == 0 {
		return task.ItemResult{}, fmt.Errorf("%w: task item %q", task.ErrNotFound, itemID)
	}
	return items[0], nil
}

func (c *Core) CancelTaskItem(ctx context.Context, taskID, itemID string) error {
	if taskID == "" {
		return fmt.Errorf("core: task id required")
	}
	if itemID == "" {
		return fmt.Errorf("core: task item id required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	controller, err := c.itemController(taskID)
	if err != nil {
		return err
	}
	return controller.cancelItem(ctx, itemID)
}

func (c *Core) CancelTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("core: task id required")
	}
	manager, err := c.taskManager()
	if err != nil {
		return err
	}
	return manager.CancelTask(ctx, id)
}

func (c *Core) RetryTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("core: task id required")
	}
	manager, err := c.taskManager()
	if err != nil {
		return err
	}
	if current, getErr := manager.GetTask(ctx, id); getErr == nil &&
		current.Type == task.TypeUploadStreamBatch &&
		(current.State == task.StateFailed || current.State == task.StatePartialFailed) &&
		c.isRecoverableUploadStreamTask(current) {
		batch, buildErr := c.uploadStreamBatchFromTask(ctx, current)
		if buildErr != nil {
			return buildErr
		}
		c.putUploadStream(batch)
		_, ok, recoverErr := manager.RecoverTask(ctx, id, func(runCtx context.Context, update task.UpdateFunc) error {
			return c.runUploadStreamTask(runCtx, update, batch)
		})
		if recoverErr != nil || !ok {
			c.removeUploadStream(id)
		}
		if recoverErr == nil && !ok {
			return fmt.Errorf("core: upload stream task %q is not recoverable", id)
		}
		return recoverErr
	}
	// A direct-upload task in retry_wait is sleeping out an exponential
	// backoff inside its batch runner. Re-running it through the manager
	// would start a second runner on the same batch; instead wake the batch
	// so it attempts again immediately.
	if batch := c.getUploadStream(id); batch != nil {
		if current, getErr := manager.GetTask(ctx, id); getErr == nil &&
			current.Type == task.TypeUploadStreamDirect &&
			current.State == task.StateRetryWait {
			select {
			case batch.retrySignal <- struct{}{}:
			default:
			}
			return nil
		}
	}
	return manager.RetryTask(ctx, id)
}

func (c *Core) DismissTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("core: task id required")
	}
	manager, err := c.taskManager()
	if err != nil {
		return err
	}
	return manager.DismissTask(ctx, id)
}

func (c *Core) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	manager, err := c.taskManager()
	if err != nil {
		return 0, err
	}
	return manager.DismissFinishedTasks(ctx, filter)
}

func (c *Core) CreateTask(ctx context.Context, req task.Request) (task.Task, error) {
	switch req.Type {
	case task.TypeUploadRemote, task.TypeUploadBatch:
		return c.createUploadTask(ctx, req)
	case task.TypeUploadStreamBatch:
		return c.createUploadStreamTask(ctx, req)
	case task.TypeUploadStreamDirect:
		return c.createUploadStreamDirectTask(ctx, req)
	case task.TypeDownload:
		return c.createDownloadTask(ctx, req)
	case task.TypeDownloadStreamBatch:
		return c.createDownloadStreamTask(ctx, req)
	case task.TypeDeleteRemote, task.TypeDeleteBatch:
		return c.createDeleteTask(ctx, req)
	case task.TypeCopy:
		return c.createCopyTask(ctx, req)
	case task.TypeMoveRemote:
		move, err := moveSpecFromTaskRequest(req)
		if err != nil {
			return task.Task{}, err
		}
		return c.createMoveTask(ctx, move)
	default:
		return task.Task{}, fmt.Errorf("core: unsupported task type %q", req.Type)
	}
}

func applyTaskRequestMetadata(item *task.Task, req task.Request) {
	item.OperationKey = req.OperationKey
	item.OperationFingerprint = req.OperationFingerprint
}

func (c *Core) CreateOperation(ctx context.Context, req task.OperationRequest) (task.Task, error) {
	if err := req.Validate(); err != nil {
		return task.Task{}, err
	}
	legacy, err := taskRequestForOperation(req)
	if err != nil {
		return task.Task{}, err
	}
	legacy.OperationKey = req.Idempotency
	legacy.OperationFingerprint, err = operationFingerprint(req)
	if err != nil {
		return task.Task{}, err
	}
	return c.CreateTask(ctx, legacy)
}

func taskRequestForOperation(req task.OperationRequest) (task.Request, error) {
	taskType := task.Type("")
	switch req.Operation {
	case task.OperationUpload:
		if req.UploadSource == task.UploadSourceLocal {
			taskType = task.TypeUploadRemote
		} else if req.UploadPolicy == task.UploadPolicyStagingOnly {
			taskType = task.TypeUploadStreamBatch
		} else {
			taskType = task.TypeUploadStreamDirect
		}
	case task.OperationDownload:
		taskType = task.TypeDownloadStreamBatch
	case task.OperationDelete:
		taskType = task.TypeDeleteRemote
		if len(req.Items) > 1 {
			taskType = task.TypeDeleteBatch
		}
	case task.OperationCopy:
		taskType = task.TypeCopy
	case task.OperationMove:
		taskType = task.TypeMoveRemote
	default:
		return task.Request{}, fmt.Errorf("%w: unsupported operation %q", task.ErrInvalidOperation, req.Operation)
	}
	return task.Request{
		Type:    taskType,
		Scope:   req.Scope,
		Items:   req.Items,
		Options: req.Options,
		Detail:  req.Detail,
	}, nil
}

func operationFingerprint(req task.OperationRequest) (string, error) {
	req.Idempotency = ""
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("core: fingerprint task operation: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func moveSpecFromTaskRequest(req task.Request) (moveTaskSpec, error) {
	if len(req.Items) != 1 {
		return moveTaskSpec{}, fmt.Errorf("core: move task requires exactly one item")
	}
	item := req.Items[0]
	if item.SourcePath == "" || item.DestPath == "" {
		return moveTaskSpec{}, fmt.Errorf("core: move task item requires source_path and dest_path")
	}
	return moveTaskSpec{
		SourcePath:  item.SourcePath,
		DestPath:    item.DestPath,
		Overwrite:   req.Options.Overwrite,
		Recursive:   req.Options.Recursive,
		Concurrency: req.Options.Concurrency,
	}, nil
}
