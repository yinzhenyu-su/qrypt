package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"sort"
	"time"
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

type uploadTaskRecord struct {
	id             string
	mount          string
	path           string
	name           string
	state          string
	bytesTotal     int64
	bytesUploaded  int64
	startedAt      time.Time
	updatedAt      time.Time
	completedAt    time.Time
	retryCount     int
	lastError      string
	lastAttemptAt  int64
	nextAttemptAt  int64
	parentRemoteID string
	resultRemoteID string
	instant        bool
	localPath      string
}

func uploadTaskRecordFromSnapshot(upload UploadSnapshot) uploadTaskRecord {
	record := uploadTaskRecord{
		id:             upload.OpID,
		mount:          upload.Mount,
		path:           upload.Path,
		name:           upload.Name,
		state:          upload.State,
		bytesTotal:     upload.BytesTotal,
		bytesUploaded:  upload.BytesUploaded,
		startedAt:      upload.StartedAt,
		updatedAt:      upload.UpdatedAt,
		completedAt:    upload.CompletedAt,
		retryCount:     upload.RetryCount,
		lastError:      upload.LastError,
		lastAttemptAt:  upload.LastAttemptAt,
		nextAttemptAt:  upload.NextAttemptAt,
		parentRemoteID: upload.ParentRemoteID,
		resultRemoteID: upload.ResultRemoteID,
		instant:        upload.Instant,
	}
	if upload.Extra != nil {
		if localPath, ok := upload.Extra["local_path"].(string); ok {
			record.localPath = localPath
		}
	}
	return record
}

func taskFromUploadRecord(upload uploadTaskRecord) task.Task {
	state := taskStateFromUpload(upload.state)
	updatedAt := upload.updatedAt
	if updatedAt.IsZero() {
		updatedAt = timeFromUnixNano(upload.nextAttemptAt)
	}
	if updatedAt.IsZero() {
		updatedAt = timeFromUnixNano(upload.lastAttemptAt)
	}
	detail := map[string]any{
		"phase":            upload.state,
		"parent_remote_id": upload.parentRemoteID,
		"result_remote_id": upload.resultRemoteID,
		"instant":          upload.instant,
		"local_path":       upload.localPath,
	}
	item := task.Task{
		ID:          upload.id,
		Type:        task.TypeUploadRemote,
		State:       state,
		Scope:       task.ScopeSync,
		Mount:       upload.mount,
		Path:        upload.path,
		Name:        upload.name,
		StartedAt:   upload.startedAt,
		UpdatedAt:   updatedAt,
		CompletedAt: upload.completedAt,
		RetryCount:  upload.retryCount,
		NextAttempt: timeFromUnixNano(upload.nextAttemptAt),
		Detail:      compactTaskDetail(detail),
	}
	item.Progress = task.Progress{
		CloudBytesDone:  upload.bytesUploaded,
		CloudBytesTotal: upload.bytesTotal,
		Phase:           upload.state,
	}
	item.Capabilities = task.Capabilities{
		Cancelable:  state != task.StateSucceeded && state != task.StateCanceled,
		Retryable:   state == task.StateFailed || state == task.StateRetryWait,
		Dismissible: isVFSTaskTerminalState(state) && !upload.completedAt.IsZero(),
		Persistent:  true,
	}
	if upload.lastError != "" {
		// Code carries the stable error category (auth, not_found, ...) so
		// clients can branch without parsing Message text; Message keeps the
		// full diagnostic detail for humans.
		item.Error = &task.Error{
			Code:      drive.ErrorCategoryMessage(upload.lastError),
			Message:   upload.lastError,
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

func timeFromUnixNano(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
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
func (s *UploadService) TaskRecords(pending []PendingUpload) []uploadTaskRecord {
	active := map[string]uploadTaskRecord{}
	s.debug.mu.Lock()
	for path, state := range s.debug.active {
		active[path] = uploadTaskRecordFromSnapshot(state.upload)
	}
	history := make([]uploadTaskRecord, 0, len(s.debug.history))
	for _, upload := range s.debug.history {
		history = append(history, uploadTaskRecordFromSnapshot(upload))
	}
	s.debug.mu.Unlock()

	timerPaths := map[string]bool{}
	s.schedule.mu.Lock()
	for path := range s.schedule.timers {
		timerPaths[path] = true
	}
	s.schedule.mu.Unlock()

	records := make([]uploadTaskRecord, 0, len(pending)+len(active)+len(history))
	seenPath := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			records = append(records, upload)
			seenPath[item.Path] = true
			continue
		}
		state := "queued"
		if item.PermanentFail {
			state = "failed"
		} else if timerPaths[item.Path] {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > timeutil.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		records = append(records, uploadTaskRecord{
			id:             item.FID,
			path:           item.Path,
			name:           item.Name,
			state:          state,
			bytesTotal:     item.Size,
			updatedAt:      timeFromUnixNano(item.UpdatedAt),
			retryCount:     item.RetryCount,
			lastError:      item.LastError,
			lastAttemptAt:  item.LastAttemptAt,
			nextAttemptAt:  item.NextAttemptAt,
			parentRemoteID: item.ParentID,
			localPath:      item.LocalPath,
		})
		seenPath[item.Path] = true
	}
	for path, upload := range active {
		if !seenPath[path] {
			records = append(records, upload)
		}
	}
	records = append(records, history...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].updatedAt.Equal(records[j].updatedAt) {
			return records[i].path < records[j].path
		}
		return records[i].updatedAt.After(records[j].updatedAt)
	})
	return records
}
