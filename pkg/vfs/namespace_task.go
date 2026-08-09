package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"strings"
)

// dismisser is satisfied by VFS instances that support task dismissal.

// Tasks returns all tasks known across all mounts.
func (n *Namespace) Tasks(filter task.Filter) []task.Task {
	return n.tasks(filter)
}

func (n *Namespace) tasks(filter task.Filter) []task.Task {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var tasks []task.Task
	for name, fs := range n.mounts {
		mountFilter, ok := namespaceMountTaskFilter(filter, name)
		if !ok {
			continue
		}
		for _, item := range fs.tasks(mountFilter) {
			item = namespaceTask(name, item)
			if filter.Match(item) {
				tasks = append(tasks, item)
			}
		}
	}
	sortTasks(tasks)
	if filter.Limit > 0 && len(tasks) > filter.Limit {
		tasks = tasks[:filter.Limit]
	}
	return tasks
}

func (n *Namespace) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	return n.listTasks(ctx, filter)
}

func (n *Namespace) listTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return n.tasks(filter), nil
}

// dismisser is satisfied by VFS instances that support task dismissal.

func (n *Namespace) DismissTask(ctx context.Context, id string) error {
	return n.dismissTask(ctx, id)
}

func (n *Namespace) dismissTask(ctx context.Context, id string) error {
	mount, rest, ok := splitNamespaceTaskID(id)
	if !ok {
		return task.ErrNotFound
	}
	n.mu.RLock()
	fs, ok := n.mounts[mount]
	n.mu.RUnlock()
	if !ok {
		return task.ErrNotFound
	}
	return fs.dismissTask(ctx, rest)
}

func (n *Namespace) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	return n.dismissFinishedTasks(ctx, filter)
}

func (n *Namespace) dismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	removed := 0
	for name, fs := range n.mounts {
		mountFilter, ok := namespaceMountTaskFilter(filter, name)
		if !ok {
			continue
		}
		n, err := fs.dismissFinishedTasks(ctx, mountFilter)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

func (n *Namespace) GetTask(ctx context.Context, id string) (task.Task, error) {
	return n.getTask(ctx, id)
}

func (n *Namespace) getTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	for _, item := range n.tasks(task.Filter{}) {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func namespaceMountTaskFilter(filter task.Filter, mount string) (task.Filter, bool) {
	if filter.Mount != "" && filter.Mount != mount {
		return task.Filter{}, false
	}
	mountFilter := filter
	if filter.Path != "" {
		mountName, rest, root := splitNamespacePath(filter.Path)
		if root || mountName != mount {
			return task.Filter{}, false
		}
		mountFilter.Path = rest
	}
	mountFilter.Mount = ""
	return mountFilter, true
}

func namespaceTask(mount string, item task.Task) task.Task {
	item.Mount = mount
	item.ID = namespaceTaskID(mount, item.ID)
	item.Path = joinVirtual("/"+mount, strings.TrimPrefix(item.Path, "/"))
	return item
}

func namespaceTaskID(mount, id string) string {
	if mount == "" || id == "" {
		return id
	}
	return mount + ":" + id
}

func splitNamespaceTaskID(id string) (string, string, bool) {
	mount, local, ok := strings.Cut(id, ":")
	if !ok || mount == "" || local == "" {
		return "", "", false
	}
	return mount, local, true
}

func (n *Namespace) CancelTask(ctx context.Context, id string) error {
	return n.cancelTask(ctx, id)
}

func (n *Namespace) cancelTask(ctx context.Context, id string) error {
	return n.applyTaskAction(ctx, id, func(fs *VFS, localID string) error {
		return fs.cancelTask(ctx, localID)
	})
}

func (n *Namespace) RetryTask(ctx context.Context, id string) error {
	return n.retryTask(ctx, id)
}

func (n *Namespace) retryTask(ctx context.Context, id string) error {
	return n.applyTaskAction(ctx, id, func(fs *VFS, localID string) error {
		return fs.retryTask(ctx, localID)
	})
}

func (n *Namespace) applyTaskAction(ctx context.Context, id string, fn func(*VFS, string) error) error {
	if mountName, localID, ok := splitNamespaceTaskID(id); ok {
		n.mu.RLock()
		fs := n.mounts[mountName]
		n.mu.RUnlock()
		if fs == nil {
			return task.ErrNotFound
		}
		return fn(fs, localID)
	}
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, fs := range n.mounts {
		mounts = append(mounts, fs)
	}
	n.mu.RUnlock()
	for _, fs := range mounts {
		if err := fn(fs, id); err == nil {
			return nil
		} else if !isTaskNotFound(err) {
			return err
		}
	}
	return task.ErrNotFound
}

// MountSpace pairs a mount name with its space usage. Err is set when the
// underlying driver does not support space queries.
