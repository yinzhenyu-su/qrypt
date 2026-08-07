package vfs

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TaskSource returns a task.Source that exposes the VFS's upload and delete
// task state. Core uses this to aggregate task records from file-system-level
// activity (FUSE writes, internal deletes) into the task manager without
// a type assertion on the VFS itself.
func (v *VFS) TaskSource() task.Source {
	return &vfsTaskSourceAdapter{v: v}
}

// vfsTaskSourceAdapter implements task.Source for a single-drive VFS.
type vfsTaskSourceAdapter struct {
	v *VFS
}

func (a *vfsTaskSourceAdapter) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.v.tasks(filter), nil
}

func (a *vfsTaskSourceAdapter) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	return a.v.getTask(ctx, id)
}

func (a *vfsTaskSourceAdapter) CancelTask(ctx context.Context, id string) error {
	return a.v.cancelTask(ctx, id)
}

func (a *vfsTaskSourceAdapter) RetryTask(ctx context.Context, id string) error {
	return a.v.retryTask(ctx, id)
}

// DismissTask is available via Core's optional taskDismisser interface.
func (a *vfsTaskSourceAdapter) DismissTask(ctx context.Context, id string) error {
	return a.v.dismissTask(ctx, id)
}

// DismissTask and DismissFinishedTasks are not on task.Source; they live
// on an optional dismisser interface that Core also checks for.

type vfsDismisser struct {
	v *VFS
}

func (d *vfsDismisser) DismissTask(ctx context.Context, id string) error {
	return d.v.dismissTask(ctx, id)
}

func (d *vfsDismisser) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	return d.v.dismissFinishedTasks(ctx, filter)
}

// TaskDismisser returns an optional dismiss interface. Consumers use a
// type assertion to check for DismissTask support, same as before.
func (v *VFS) TaskDismisser() interface {
	DismissTask(ctx context.Context, id string) error
	DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error)
} {
	return &vfsDismisser{v: v}
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

func (n *Namespace) TaskDismisser() interface {
	DismissTask(ctx context.Context, id string) error
	DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error)
} {
	return &namespaceDismisserAdapter{n: n}
}

type namespaceDismisserAdapter struct {
	n *Namespace
}

func (d *namespaceDismisserAdapter) DismissTask(ctx context.Context, id string) error {
	return d.n.dismissTask(ctx, id)
}

func (d *namespaceDismisserAdapter) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	return d.n.dismissFinishedTasks(ctx, filter)
}
