package vfs

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	idelete "github.com/yinzhenyu/qrypt/internal/vfs/delete"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"path"
	"sort"
	"strings"
	"time"
)

const deleteTaskPrefix = "delete:"

type deleteTaskSource struct {
	runtime vfsDeleteTaskRuntime
}

type deleteTaskRecord struct {
	id        string
	path      string
	entry     drive.Entry
	state     task.State
	phase     string
	errorText string
	updatedAt time.Time
}

func (s deleteTaskSource) List(filter task.Filter) []task.Task {
	records := s.records()
	tasks := make([]task.Task, 0, len(records))
	for _, record := range records {
		item := taskFromDeleteRecord(record)
		if filter.Match(item) {
			tasks = append(tasks, item)
		}
	}
	sortTasks(tasks)
	return tasks
}

func (s deleteTaskSource) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := s.recordByID(id)
	if !ok {
		return ErrNotFound
	}
	if record.state == task.StateRunning {
		return fmt.Errorf("vfs: delete task %q is running", id)
	}
	if _, ok := s.runtime.Restore(record.path); !ok {
		return ErrNotFound
	}
	return nil
}

func (s deleteTaskSource) Retry(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := s.recordByID(id)
	if !ok {
		return ErrNotFound
	}
	if record.state != task.StateFailed {
		return fmt.Errorf("vfs: delete task %q is not retryable", id)
	}
	s.runtime.Retry(record)
	return nil
}

func (s deleteTaskSource) Dismiss(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := s.recordByID(id)
	if !ok {
		return ErrNotFound
	}
	if record.state == task.StateScheduled || record.state == task.StateFailed {
		return s.Cancel(ctx, id)
	}
	if record.state != task.StateSucceeded && record.state != task.StateCanceled {
		return fmt.Errorf("vfs: delete task %q is not dismissible", id)
	}
	return ErrNotFound
}

func (s deleteTaskSource) DismissFinished(ctx context.Context, filter task.Filter) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (s deleteTaskSource) recordByID(id string) (deleteTaskRecord, bool) {
	for _, record := range s.records() {
		if record.id == id {
			return record, true
		}
	}
	return deleteTaskRecord{}, false
}

func (s deleteTaskSource) records() []deleteTaskRecord {
	records := s.runtime.Records()
	sort.Slice(records, func(i, j int) bool {
		return records[i].path < records[j].path
	})
	return records
}

func taskFromDeleteRecord(record deleteTaskRecord) task.Task {
	item := task.Task{
		ID:        record.id,
		Type:      task.TypeDeleteRemote,
		State:     record.state,
		Scope:     task.ScopeSync,
		Path:      record.path,
		Name:      path.Base(record.path),
		UpdatedAt: record.updatedAt,
		Progress: task.Progress{
			ItemsTotal:  1,
			CurrentPath: record.path,
			Phase:       record.phase,
		},
		Capabilities: task.Capabilities{
			Cancelable: record.state == task.StateScheduled || record.state == task.StateFailed,
			Retryable:  record.state == task.StateFailed,
			Persistent: true,
		},
		Detail: compactTaskDetail(map[string]any{
			"phase":     record.phase,
			"remote_id": record.entry.ID,
			"is_dir":    record.entry.IsDir,
		}),
	}
	if record.state == task.StateFailed {
		item.Progress.ItemsDone = 1
		item.Progress.ItemsFailed = 1
		message := record.errorText
		if message == "" {
			message = "remote delete did not complete"
		}
		item.Error = &task.Error{Message: message, Retryable: true}
	} else if record.state == task.StateRunning {
		item.Progress.ItemsDone = 0
	}
	return item
}

func deleteTaskID(entry drive.Entry, p string) string {
	if entry.ID != "" {
		return deleteTaskPrefix + entry.ID
	}
	return deleteTaskPrefix + strings.TrimPrefix(cleanVirtual(p), "/")
}

type vfsDeleteTaskRuntime struct {
	v *VFS
}

func newVFSDeleteTaskRuntime(v *VFS) vfsDeleteTaskRuntime {
	return vfsDeleteTaskRuntime{v: v}
}

func (r vfsDeleteTaskRuntime) Records() []deleteTaskRecord {
	now := timeutil.Now()
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	records := make([]deleteTaskRecord, 0, len(r.v.view.overlay.deleted))
	for p, entry := range r.v.view.overlay.deleted {
		state, phase := r.deleteStateLocked(p)
		records = append(records, deleteTaskRecord{
			id:        deleteTaskID(entry, p),
			path:      p,
			entry:     entry,
			state:     state,
			phase:     phase,
			errorText: r.v.deletes.tasks.failures[p],
			updatedAt: now,
		})
	}
	return records
}

func (r vfsDeleteTaskRuntime) Restore(path string) (drive.Entry, bool) {
	return r.v.restoreDeletedPath(path)
}

func (r vfsDeleteTaskRuntime) Retry(record deleteTaskRecord) {
	r.v.view.overlay.mu.Lock()
	delete(r.v.deletes.tasks.failures, record.path)
	r.v.view.overlay.mu.Unlock()
	r.v.scheduleDelete(record.path, record.entry)
}

func (r vfsDeleteTaskRuntime) deleteStateLocked(path string) (task.State, string) {
	if _, ok := r.v.deletes.tasks.active[path]; ok {
		return task.StateRunning, "delete"
	}
	if _, ok := r.v.deletes.tasks.scheduler.Keys()[path]; ok {
		return task.StateScheduled, "scheduled"
	}
	return task.StateFailed, "failed"
}

// DeleteService groups the VFS delete-domain state: the debounce tasks and
// the delete delay. Owned by the delete scheduler; initialized in New.
type DeleteService struct {
	tasks *deleteTaskState
	delay time.Duration
}

// newDeleteState builds the delete domain state together.
func newDeleteService(tasks *deleteTaskState, delay time.Duration) *DeleteService {
	return &DeleteService{tasks: tasks, delay: delay}
}

// Close stops the pending delete timers. Called by the VFS lifecycle;
// in-flight deletes run on the VFS lifecycle context and cancel with it.
func (d *DeleteService) Close() {
	d.tasks.stopAll()
}

// Compile-time interface satisfaction check.
var _ idelete.Service = (*DeleteService)(nil)
