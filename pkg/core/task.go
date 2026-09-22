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

// Task-facing client contracts. pkg/task is the task implementation owned
// below pkg/core; mobile consumes task DTOs only through these aliases (and
// TaskEvent/TaskSubscription in task_events.go), never through pkg/task.
// Aliasing keeps the mobile JSON wire format identical to pkg/task by
// construction; task_contract_test.go pins the schema.
type Task = task.Task
type TaskFilter = task.Filter
type TaskItem = task.Item
type TaskItemFilter = task.ItemFilter
type TaskItemResult = task.ItemResult
type TaskOptions = task.Options
type TaskOperationRequest = task.OperationRequest
type TaskRequest = task.Request
type TaskState = task.State
type TaskType = task.Type

// TaskScopeUser is the scope mobile task lists default to: user-origin tasks
// only, keeping internal-scope bookkeeping records out of the UI list.
const TaskScopeUser = task.ScopeUser

// TaskScopeInternal is the origin of mount write-path bookkeeping records
// (upload_remote/delete_remote projections). App default lists exclude it;
// filtering by this scope surfaces the bookkeeping records.
const TaskScopeInternal = task.ScopeInternal

// TaskTypeUploadStreamBatch is the staging upload task type mobile creates
// through task requests when it streams a local file into qrypt.
const TaskTypeUploadStreamBatch = task.TypeUploadStreamBatch

// taskDismisser is the optional task-dismissal capability upload tasks use
// when their source exposes it (a Core session). Named so fakes are trivial.
type taskDismisser interface {
	DismissTask(context.Context, string) error
}

func (c *Core) newTaskManager() *task.Manager {
	sources := []task.Source{c.fs.TaskSource()}
	store := c.taskStore()
	manager := task.NewManagerWithStore(store, sources...)
	for _, typ := range task.RecoverableTypes() {
		if recovery := c.taskRecovery(typ); recovery != nil {
			recovery(context.Background(), manager)
		}
	}
	return manager
}

// taskRecovery returns the startup recovery for a type declared recoverable.
// taskRecoveryPaths is asserted against the declared recoverable types, so a
// type cannot be declared recoverable without a recovery implementation.
func (c *Core) taskRecovery(typ task.Type) func(context.Context, *task.Manager) {
	recovery, ok := taskRecoveryPaths[typ]
	if !ok {
		return nil
	}
	return func(ctx context.Context, manager *task.Manager) { recovery(c, ctx, manager) }
}

var taskRecoveryPaths = map[task.Type]func(*Core, context.Context, *task.Manager){
	task.TypeUploadStreamBatch:  (*Core).recoverUploadStreamTasks,
	task.TypeUploadStreamDirect: (*Core).recoverUploadStreamDirectTasks,
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
	current, currentErr := manager.GetTask(ctx, id)
	strategy := task.RetryManager
	if currentErr == nil {
		if descriptor, ok := task.Describe(current.Type); ok {
			strategy = descriptor.Retry
		}
	}
	if strategy == task.RetryRecover && currentErr == nil &&
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
	if strategy == task.RetryWakeRunner {
		if batch := c.getUploadStream(id); batch != nil && currentErr == nil && current.State == task.StateRetryWait {
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
	descriptor, ok := task.Describe(req.Type)
	if !ok {
		return task.Task{}, fmt.Errorf("core: unsupported task type %q", req.Type)
	}
	create, ok := taskCreators[descriptor.Creation]
	if !ok {
		return task.Task{}, fmt.Errorf("core: task type %q has no creation path", req.Type)
	}
	return create(c, ctx, req)
}

// taskCreators maps every declared creation path to its implementation. The
// test suite asserts the keys match the paths declared by the task type
// descriptors exactly, so a type cannot be declared without a creation path
// (or the reverse).
var taskCreators = map[task.CreationPath]func(*Core, context.Context, task.Request) (task.Task, error){
	task.CreationUpload:             (*Core).createUploadTask,
	task.CreationUploadStream:       (*Core).createUploadStreamTask,
	task.CreationUploadStreamDirect: (*Core).createUploadStreamDirectTask,
	task.CreationDownload:           (*Core).createDownloadTask,
	task.CreationDownloadStream:     (*Core).createDownloadStreamTask,
	task.CreationDelete:             (*Core).createDeleteTask,
	task.CreationCopy:               (*Core).createCopyTask,
	task.CreationMove:               createMoveTaskFromRequest,
}

// createMoveTaskFromRequest adapts a move request to the move creation path,
// which works on a resolved move spec.
func createMoveTaskFromRequest(c *Core, ctx context.Context, req task.Request) (task.Task, error) {
	move, err := moveSpecFromTaskRequest(req)
	if err != nil {
		return task.Task{}, err
	}
	return c.createMoveTask(ctx, move)
}

// taskCreationCapabilities returns the capability defaults declared for a task
// type. Create functions apply runtime-derived upgrades (a move that crosses
// mounts, a recursive download) on top of it.
func taskCreationCapabilities(typ task.Type) task.Capabilities {
	descriptor, _ := task.Describe(typ)
	return descriptor.CreationCapabilities()
}

func applyTaskRequestMetadata(item *task.Task, req task.Request) {
	item.IdempotencyKey = req.IdempotencyKey
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
	legacy.IdempotencyKey = req.IdempotencyKey
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
		taskType = task.Promote(task.TypeDownloadStreamBatch, len(req.Items))
	case task.OperationDelete:
		taskType = task.Promote(task.TypeDeleteRemote, len(req.Items))
	case task.OperationCopy:
		taskType = task.Promote(task.TypeCopy, len(req.Items))
	case task.OperationMove:
		taskType = task.Promote(task.TypeMoveRemote, len(req.Items))
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
	req.IdempotencyKey = ""
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("core: fingerprint task operation: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func moveSpecFromTaskRequest(req task.Request) (moveTaskSpec, error) {
	if len(req.Items) == 0 {
		return moveTaskSpec{}, fmt.Errorf("core: move task requires at least one item")
	}
	items := make([]task.Item, len(req.Items))
	for i, item := range req.Items {
		if item.SourcePath == "" || item.DestPath == "" {
			return moveTaskSpec{}, fmt.Errorf("core: move task item requires source_path and dest_path")
		}
		items[i] = item
	}
	return moveTaskSpec{
		Type:        req.Type,
		Items:       items,
		Overwrite:   req.Options.Overwrite,
		Recursive:   req.Options.Recursive,
		Concurrency: req.Options.Concurrency,
	}, nil
}
