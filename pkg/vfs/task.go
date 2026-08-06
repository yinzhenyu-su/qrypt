package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"sort"
	"time"
)

func (v *VFS) Tasks(filter task.Filter) []task.Task {
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.Tasks(filter), nil
}

func (v *VFS) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	for _, item := range v.Tasks(task.Filter{}) {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (v *VFS) CancelTask(ctx context.Context, id string) error {
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
		uploadTaskSource{runtime: newVFSUploadTaskRuntime(v)},
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

type uploadTaskSource struct {
	runtime uploadTaskRuntime
}

func (s uploadTaskSource) List(filter task.Filter) []task.Task {
	uploads := s.runtime.Records()
	tasks := make([]task.Task, 0, len(uploads))
	seen := map[string]bool{}
	for _, upload := range uploads {
		if upload.id != "" && seen[upload.id] {
			continue
		}
		seen[upload.id] = true
		item := taskFromUploadRecord(upload)
		if filter.Match(item) {
			tasks = append(tasks, item)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return taskLess(tasks[i], tasks[j])
	})
	return tasks
}

func (s uploadTaskSource) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := s.runtime.PendingByID(id)
	if !ok {
		return ErrNotFound
	}
	return s.runtime.CancelAndRemove(pending.Path)
}

func (s uploadTaskSource) Retry(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := s.runtime.PendingByID(id)
	if !ok {
		return ErrNotFound
	}
	return s.runtime.Retry(pending)
}

func (s uploadTaskSource) Dismiss(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A driver reports the upload phase as "completed" before the engine
	// finishes committing (e.g. waitUploadedFile still polling), so a succeeded
	// upload can briefly still be marked active with its pending record intact.
	// Dismissing it must not cancel the upload: cancel-and-remove would drop
	// the pending record, and the engine would then delete the freshly
	// uploaded remote file as a "stale version". Drop the history entry when
	// present; while the task is still active the engine owns it.
	if state, ok := s.runtime.StateByID(id); ok && state == uploadSnapshotStateCompleted {
		_ = s.runtime.RemoveHistoryByID(id)
		return nil
	}
	// 1. Pending: cancel + remove from persistent store
	if pending, ok := s.runtime.PendingByID(id); ok {
		return s.runtime.CancelAndRemove(pending.Path)
	}
	// 2. Active: cancel + remove from persistent store (by path lookup)
	if path, ok := s.runtime.ActivePathByID(id); ok {
		return s.runtime.CancelAndRemove(path)
	}
	// 3. History: remove from in-memory history
	if !s.runtime.RemoveHistoryByID(id) {
		return ErrNotFound
	}
	return nil
}

func (s uploadTaskSource) DismissFinished(ctx context.Context, filter task.Filter) (int, error) {
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
		if s.runtime.RemoveHistoryByID(item.ID) {
			removed++
		}
	}
	return removed, nil
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

type uploadTaskRuntime interface {
	Records() []uploadTaskRecord
	PendingByID(id string) (PendingUpload, bool)
	ActivePathByID(id string) (string, bool)
	StateByID(id string) (string, bool)
	CancelAndRemove(path string) error
	Retry(pending PendingUpload) error
	RemoveHistoryByID(id string) bool
}

type vfsUploadTaskRuntime struct {
	v *VFS
}

func newVFSUploadTaskRuntime(v *VFS) vfsUploadTaskRuntime {
	return vfsUploadTaskRuntime{v: v}
}

func (r vfsUploadTaskRuntime) Records() []uploadTaskRecord {
	return r.v.uploadTaskRecords(r.v.upload.store.PendingUploads())
}

func (r vfsUploadTaskRuntime) PendingByID(id string) (PendingUpload, bool) {
	for _, pending := range r.v.upload.store.PendingUploads() {
		if pending.FID == id {
			return pending, true
		}
	}
	return PendingUpload{}, false
}

func (r vfsUploadTaskRuntime) ActivePathByID(id string) (string, bool) {
	r.v.upload.debug.mu.Lock()
	defer r.v.upload.debug.mu.Unlock()
	for path, state := range r.v.upload.debug.active {
		if state.upload.OpID == id {
			return path, true
		}
	}
	return "", false
}

func (r vfsUploadTaskRuntime) StateByID(id string) (string, bool) {
	r.v.upload.debug.mu.Lock()
	defer r.v.upload.debug.mu.Unlock()
	for _, state := range r.v.upload.debug.active {
		if state.upload.OpID == id {
			return state.upload.State, true
		}
	}
	for _, upload := range r.v.upload.debug.history {
		if upload.OpID == id {
			return upload.State, true
		}
	}
	return "", false
}

func (r vfsUploadTaskRuntime) CancelAndRemove(path string) error {
	r.v.cancelUpload(path)
	if err := r.v.upload.store.RemoveUpload(path); err != nil {
		return err
	}
	r.v.upload.hashes.removePath(path)
	return nil
}

func (r vfsUploadTaskRuntime) Retry(pending PendingUpload) error {
	now := timeutil.Now()
	if quietWindow := r.v.uploadQuietWindow(pending); quietWindow > 0 {
		pending.UpdatedAt = now.Add(-quietWindow - time.Nanosecond).UnixNano()
	} else {
		pending.UpdatedAt = now.UnixNano()
	}
	pending.PermanentFail = false
	pending.LastError = ""
	pending.NextAttemptAt = 0
	if err := r.v.upload.store.SaveUploadExact(pending); err != nil {
		return err
	}
	if latest, ok := r.v.upload.store.UploadByPath(pending.Path); ok {
		pending = latest
	}
	r.v.cancelUpload(pending.Path)
	r.v.enqueueAfter(pending, 0)
	return nil
}

func (r vfsUploadTaskRuntime) RemoveHistoryByID(id string) bool {
	return r.v.removeUploadHistoryByID(id)
}

func (v *VFS) uploadTaskRecords(pending []PendingUpload) []uploadTaskRecord {
	active := map[string]uploadTaskRecord{}
	v.upload.debug.mu.Lock()
	for path, state := range v.upload.debug.active {
		active[path] = uploadTaskRecordFromSnapshot(state.upload)
	}
	history := make([]uploadTaskRecord, 0, len(v.upload.debug.history))
	for _, upload := range v.upload.debug.history {
		history = append(history, uploadTaskRecordFromSnapshot(upload))
	}
	v.upload.debug.mu.Unlock()

	timerPaths := map[string]bool{}
	v.upload.schedule.mu.Lock()
	for path := range v.upload.schedule.timers {
		timerPaths[path] = true
	}
	v.upload.schedule.mu.Unlock()

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
