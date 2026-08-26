package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	idelete "github.com/yinzhenyu/qrypt/pkg/vfs/delete"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"path"
	"sort"
	"time"
)

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
	switch record.state {
	case task.StateFailed:
		item.Progress.ItemsDone = 1
		item.Progress.ItemsFailed = 1
		message := record.errorText
		if message == "" {
			message = "remote delete did not complete"
		}
		item.Error = &task.Error{Message: message, Retryable: true}
	case task.StateRunning:
		item.Progress.ItemsDone = 0
	}
	return item
}

// vfsDeleteTaskRuntime adapts the view-domain delete records to the task
// source: the record projection and retry/cancel live in the view package;
// this adapter maps them onto pkg/task and the VFS delete scheduler.
type vfsDeleteTaskRuntime struct {
	vis            view.Visibility
	tasks          *view.Tasks
	restoreDeleted func(string) (drive.Entry, bool)
	scheduleDelete func(string, drive.Entry)
}

func newVFSDeleteTaskRuntime(v *VFS) vfsDeleteTaskRuntime {
	return vfsDeleteTaskRuntime{
		vis:            view.NewVisibility(v.view.Overlay(), v.deletes.tasks, v.view, nil),
		tasks:          v.deletes.tasks,
		restoreDeleted: v.restoreDeletedPath,
		scheduleDelete: v.scheduleDelete,
	}
}

func (r vfsDeleteTaskRuntime) Records() []deleteTaskRecord {
	records := r.vis.DeleteTaskRecords()
	out := make([]deleteTaskRecord, 0, len(records))
	for _, record := range records {
		out = append(out, deleteTaskRecordFromView(record))
	}
	return out
}

// deleteTaskRecordFromView maps a view-domain DeleteRecord onto the task
// source record (state flags -> task.State).
func deleteTaskRecordFromView(r view.DeleteRecord) deleteTaskRecord {
	state := task.StateFailed
	phase := "failed"
	if r.Running {
		state, phase = task.StateRunning, "delete"
	} else if r.Scheduled {
		state, phase = task.StateScheduled, "scheduled"
	}
	return deleteTaskRecord{
		id:        r.ID,
		path:      r.Path,
		entry:     r.Entry,
		state:     state,
		phase:     phase,
		errorText: r.ErrorText,
		updatedAt: r.UpdatedAt,
	}
}

func (r vfsDeleteTaskRuntime) Restore(path string) (drive.Entry, bool) {
	return r.restoreDeleted(path)
}

func (r vfsDeleteTaskRuntime) Retry(record deleteTaskRecord) {
	r.tasks.ClearFailure(record.path)
	r.scheduleDelete(record.path, record.entry)
}

// DeleteService groups the VFS delete-domain state: the debounce tasks and
// the delete delay. Owned by the delete scheduler; initialized in New.
type DeleteService struct {
	tasks *view.Tasks
	delay time.Duration
}

// newDeleteService builds the delete domain state together.
func newDeleteService(tasks *view.Tasks, delay time.Duration) *DeleteService {
	return &DeleteService{tasks: tasks, delay: delay}
}

// Close stops the pending delete timers. Called by the VFS lifecycle;
// in-flight deletes run on the VFS lifecycle context and cancel with it.
func (d *DeleteService) Close() {
	d.tasks.StopAll()
}

// Compile-time interface satisfaction check.
var _ idelete.Service = (*DeleteService)(nil)
