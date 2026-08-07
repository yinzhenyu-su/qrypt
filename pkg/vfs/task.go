package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"sort"
)

// Tasks returns all upload and delete tasks known to this VFS.
func (v *VFS) Tasks(filter task.Filter) []task.Task {
	return v.tasks(filter)
}

func (v *VFS) tasks(filter task.Filter) []task.Task {
	var tasks []task.Task
	for _, source := range v.taskSources() {
		tasks = append(tasks, source.List(filter)...)
	}
	sortTasks(tasks)
	if filter.Limit > 0 && len(tasks) > filter.Limit {
		tasks = tasks[:filter.Limit]
	}
	return tasks
}

func (v *VFS) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	return v.listTasks(ctx, filter)
}

func (v *VFS) listTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.tasks(filter), nil
}

func (v *VFS) GetTask(ctx context.Context, id string) (task.Task, error) {
	return v.getTask(ctx, id)
}

func (v *VFS) getTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	for _, item := range v.tasks(task.Filter{}) {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (v *VFS) CancelTask(ctx context.Context, id string) error {
	return v.cancelTask(ctx, id)
}

func (v *VFS) cancelTask(ctx context.Context, id string) error {
	if err := v.applyTaskAction(ctx, id, func(source vfsTaskSource) error {
		return source.Cancel(ctx, id)
	}); err != nil {
		if isTaskNotFound(err) {
			return task.ErrNotFound
		}
		return err
	}
	return nil
}

func (v *VFS) RetryTask(ctx context.Context, id string) error {
	return v.retryTask(ctx, id)
}

func (v *VFS) retryTask(ctx context.Context, id string) error {
	if err := v.applyTaskAction(ctx, id, func(source vfsTaskSource) error {
		return source.Retry(ctx, id)
	}); err != nil {
		if isTaskNotFound(err) {
			return task.ErrNotFound
		}
		return err
	}
	return nil
}

func (v *VFS) DismissTask(ctx context.Context, id string) error {
	return v.dismissTask(ctx, id)
}

func (v *VFS) dismissTask(ctx context.Context, id string) error {
	if err := v.applyTaskAction(ctx, id, func(source vfsTaskSource) error {
		return source.Dismiss(ctx, id)
	}); err != nil {
		if isTaskNotFound(err) {
			return task.ErrNotFound
		}
		return err
	}
	return nil
}

func (v *VFS) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	return v.dismissFinishedTasks(ctx, filter)
}

func (v *VFS) dismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	removed := 0
	for _, source := range v.taskSources() {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		n, err := source.DismissFinished(ctx, filter)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

type vfsTaskSource interface {
	List(task.Filter) []task.Task
	Cancel(context.Context, string) error
	Retry(context.Context, string) error
	Dismiss(context.Context, string) error
	DismissFinished(context.Context, task.Filter) (int, error)
}

func (v *VFS) taskSources() []vfsTaskSource {
	return []vfsTaskSource{
		uploadServiceTaskSource{svc: v.uploads},
		deleteTaskSource{runtime: newVFSDeleteTaskRuntime(v)},
	}
}

func (v *VFS) applyTaskAction(ctx context.Context, id string, fn func(vfsTaskSource) error) error {
	for _, source := range v.taskSources() {
		if err := fn(source); err == nil {
			return nil
		} else if !isTaskNotFound(err) {
			return err
		}
	}
	return ErrNotFound
}

func taskFromUploadRecord(upload uploadTaskRecord) task.Task {
	state := taskStateFromUpload(upload.State)
	updatedAt := upload.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = timeFromUnixNano(upload.NextAttemptAt)
	}
	if updatedAt.IsZero() {
		updatedAt = timeFromUnixNano(upload.LastAttemptAt)
	}
	detail := map[string]any{
		"phase":            upload.State,
		"parent_remote_id": upload.ParentRemoteID,
		"result_remote_id": upload.ResultRemoteID,
		"instant":          upload.Instant,
		"local_path":       upload.LocalPath,
	}
	item := task.Task{
		ID:          upload.ID,
		Type:        task.TypeUploadRemote,
		State:       state,
		Scope:       task.ScopeSync,
		Mount:       upload.Mount,
		Path:        upload.Path,
		Name:        upload.Name,
		StartedAt:   upload.StartedAt,
		UpdatedAt:   updatedAt,
		CompletedAt: upload.CompletedAt,
		RetryCount:  upload.RetryCount,
		NextAttempt: timeFromUnixNano(upload.NextAttemptAt),
		Detail:      compactTaskDetail(detail),
	}
	item.Progress = task.Progress{
		CloudBytesDone:  upload.BytesUploaded,
		CloudBytesTotal: upload.BytesTotal,
		Phase:           upload.State,
	}
	item.Capabilities = task.Capabilities{
		Cancelable:  state != task.StateSucceeded && state != task.StateCanceled,
		Retryable:   state == task.StateFailed || state == task.StateRetryWait,
		Dismissible: isVFSTaskTerminalState(state) && !upload.CompletedAt.IsZero(),
		Persistent:  true,
	}
	if upload.LastError != "" {
		// Code carries the stable error category (auth, not_found, ...) so
		// clients can branch without parsing Message text; Message keeps the
		// full diagnostic detail for humans.
		item.Error = &task.Error{
			Code:      drive.ErrorCategoryMessage(upload.LastError),
			Message:   upload.LastError,
			Retryable: item.Capabilities.Retryable,
		}
	}
	if !item.UpdatedAt.IsZero() {
		item.Version = uint64(item.UpdatedAt.UnixNano())
	}
	return item
}

func taskStateFromUpload(state string) task.State {
	switch state {
	case "queued":
		return task.StateQueued
	case "scheduled":
		return task.StateScheduled
	case "retry_wait":
		return task.StateRetryWait
	case uploadSnapshotStateCompleted:
		return task.StateSucceeded
	case uploadSnapshotStateFailed:
		return task.StateFailed
	case "canceled":
		return task.StateCanceled
	case "":
		return task.StateQueued
	default:
		return task.StateRunning
	}
}

func isVFSTaskTerminalState(state task.State) bool {
	switch state {
	case task.StateSucceeded, task.StatePartialFailed, task.StateFailed, task.StateCanceled:
		return true
	default:
		return false
	}
}

func compactTaskDetail(detail map[string]any) map[string]any {
	for key, value := range detail {
		switch v := value.(type) {
		case string:
			if v == "" {
				delete(detail, key)
			}
		case bool:
			if !v {
				delete(detail, key)
			}
		case nil:
			delete(detail, key)
		}
	}
	if len(detail) == 0 {
		return nil
	}
	return detail
}

func sortTasks(tasks []task.Task) {
	sort.Slice(tasks, func(i, j int) bool {
		return taskLess(tasks[i], tasks[j])
	})
}

func taskLess(a, b task.Task) bool {
	aActive := !isVFSTaskTerminalState(a.State)
	bActive := !isVFSTaskTerminalState(b.State)
	if aActive != bActive {
		return aActive
	}
	aTime := a.CreatedAt
	if aTime.IsZero() {
		aTime = a.StartedAt
	}
	if aTime.IsZero() {
		aTime = a.UpdatedAt
	}
	bTime := b.CreatedAt
	if bTime.IsZero() {
		bTime = b.StartedAt
	}
	if bTime.IsZero() {
		bTime = b.UpdatedAt
	}
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	return a.ID < b.ID
}

func isTaskNotFound(err error) bool {
	return err == ErrNotFound || err == task.ErrNotFound
}

// TaskRecords builds upload task records from pending uploads combined with
// active debug state and history. This is the data source for the VFS task
// listing; Core can use it via TaskSource without accessing upload internals.
