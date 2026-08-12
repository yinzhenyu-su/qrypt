package vfs

import (
	"context"
	"sort"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TaskSource returns a task.Source backed by the upload and delete services.
func (v *VFS) TaskSource() task.Source {
	return &vfsTaskSourceAdapter{
		uploads: v.uploads,
		deletes: deleteTaskSource{runtime: newVFSDeleteTaskRuntime(v)},
	}
}

// vfsTaskSourceAdapter implements task.Source for a single-drive VFS.
// It delegates to service-level task sources: uploadServiceTaskSource
// and deleteTaskSource.
type vfsTaskSourceAdapter struct {
	uploads *uploadService
	deletes deleteTaskSource
}

func (a *vfsTaskSourceAdapter) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var all []task.Task
	all = append(all, uploadServiceTaskSource{svc: a.uploads}.List(filter)...)
	all = append(all, a.deletes.List(filter)...)
	sortTasks(all)
	if filter.Limit > 0 && len(all) > filter.Limit {
		all = all[:filter.Limit]
	}
	return all, nil
}

func (a *vfsTaskSourceAdapter) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	for _, src := range []vfsTaskSource{
		uploadServiceTaskSource{svc: a.uploads},
		a.deletes,
	} {
		for _, item := range src.List(task.Filter{}) {
			if item.ID == id {
				return item, nil
			}
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (a *vfsTaskSourceAdapter) CancelTask(ctx context.Context, id string) error {
	return a.apply(id, func(src vfsTaskSource) error { return src.Cancel(ctx, id) })
}

func (a *vfsTaskSourceAdapter) RetryTask(ctx context.Context, id string) error {
	return a.apply(id, func(src vfsTaskSource) error { return src.Retry(ctx, id) })
}

func (a *vfsTaskSourceAdapter) DismissTask(ctx context.Context, id string) error {
	return a.apply(id, func(src vfsTaskSource) error { return src.Dismiss(ctx, id) })
}

func (a *vfsTaskSourceAdapter) apply(id string, fn func(vfsTaskSource) error) error {
	for _, src := range []vfsTaskSource{
		uploadServiceTaskSource{svc: a.uploads},
		a.deletes,
	} {
		if err := fn(src); err == nil {
			return nil
		} else if !isTaskNotFound(err) {
			return err
		}
	}
	return ErrNotFound
}

// uploadServiceTaskSource implements vfsTaskSource backed by *uploadService.
type uploadServiceTaskSource struct {
	svc *uploadService
}

func (s uploadServiceTaskSource) List(filter task.Filter) []task.Task {
	records := s.svc.TaskRecords(s.svc.PendingUploads())
	tasks := make([]task.Task, 0, len(records))
	seen := map[string]bool{}
	for _, r := range records {
		if r.ID != "" && seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		item := taskFromUploadRecord(r)
		if filter.Match(item) {
			tasks = append(tasks, item)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return taskLess(tasks[i], tasks[j])
	})
	return tasks
}

func (s uploadServiceTaskSource) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := s.svc.PendingByID(id)
	if !ok {
		return ErrNotFound
	}
	return s.cancelAndRemove(pending.Path)
}

func (s uploadServiceTaskSource) cancelAndRemove(path string) error {
	s.svc.CancelUpload(path)
	if err := s.svc.RemoveUpload(path); err != nil {
		return err
	}
	s.svc.HashRemovePath(path)
	return nil
}

func (s uploadServiceTaskSource) Retry(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := s.svc.PendingByID(id)
	if !ok {
		return ErrNotFound
	}
	return s.svc.Retry(pending)
}

func (s uploadServiceTaskSource) Dismiss(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Completed but still active: drop history without cancelling.
	if state, ok := s.svc.DebugStateByID(id); ok && state == uploadSnapshotStateCompleted {
		_ = s.svc.RemoveHistoryByID(id)
		return nil
	}
	// Pending
	if pending, ok := s.svc.PendingByID(id); ok {
		return s.cancelAndRemove(pending.Path)
	}
	// Active (by path lookup)
	if path, ok := s.svc.DebugActivePathByID(id); ok {
		return s.cancelAndRemove(path)
	}
	// History
	if !s.svc.RemoveHistoryByID(id) {
		return ErrNotFound
	}
	return nil
}

func (s uploadServiceTaskSource) DismissFinished(ctx context.Context, filter task.Filter) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	removed := 0
	for _, item := range s.List(filter) {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !isVFSTaskTerminalState(item.State) || !item.Capabilities.Dismissible {
			continue
		}
		if s.svc.RemoveHistoryByID(item.ID) {
			removed++
		}
	}
	return removed, nil
}

// --- Namespace equivalents ---

func (n *Namespace) TaskSource() task.Source {
	return &namespaceTaskSourceAdapter{n: n}
}

type namespaceTaskSourceAdapter struct {
	n *Namespace
}

func (a *namespaceTaskSourceAdapter) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.n.tasks(filter), nil
}

func (a *namespaceTaskSourceAdapter) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	return a.n.getTask(ctx, id)
}

func (a *namespaceTaskSourceAdapter) CancelTask(ctx context.Context, id string) error {
	return a.n.cancelTask(ctx, id)
}

func (a *namespaceTaskSourceAdapter) RetryTask(ctx context.Context, id string) error {
	return a.n.retryTask(ctx, id)
}

func (a *namespaceTaskSourceAdapter) DismissTask(ctx context.Context, id string) error {
	return a.n.dismissTask(ctx, id)
}
