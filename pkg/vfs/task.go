package vfs

import (
	"context"
	"sort"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func (v *VFS) Tasks(filter task.Filter) []task.Task {
	pending := v.cache.Pending()
	uploads := v.uploadSnapshots(pending)
	uploads = append(uploads, v.uploadSnapshotHistory()...)
	tasks := make([]task.Task, 0, len(uploads))
	seen := map[string]bool{}
	for _, upload := range uploads {
		if upload.OpID != "" && seen[upload.OpID] {
			continue
		}
		seen[upload.OpID] = true
		item := taskFromUploadSnapshot(upload)
		if filter.Match(item) {
			tasks = append(tasks, item)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	if filter.Limit > 0 && len(tasks) > filter.Limit {
		tasks = tasks[:filter.Limit]
	}
	return tasks
}

func (v *VFS) CancelTask(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := v.pendingByTaskID(id)
	if !ok {
		return ErrNotFound
	}
	v.cancelUpload(pending.Path)
	return v.cache.RemovePending(pending.Path)
}

func (v *VFS) RetryTask(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, ok := v.pendingByTaskID(id)
	if !ok {
		return ErrNotFound
	}
	now := timeutil.Now()
	if quietWindow := v.pendingQuietWindow(pending); quietWindow > 0 {
		pending.UpdatedAt = now.Add(-quietWindow - time.Nanosecond).UnixNano()
	} else {
		pending.UpdatedAt = now.UnixNano()
	}
	pending.PermanentFail = false
	pending.LastError = ""
	pending.NextAttemptAt = 0
	if err := v.cache.SavePendingExact(pending); err != nil {
		return err
	}
	if latest, ok := v.cache.PendingByPath(pending.Path); ok {
		pending = latest
	}
	v.cancelUpload(pending.Path)
	v.enqueueAfter(pending, 0)
	return nil
}

func (v *VFS) pendingByTaskID(id string) (PendingFile, bool) {
	for _, pending := range v.cache.Pending() {
		if pending.FID == id {
			return pending, true
		}
	}
	return PendingFile{}, false
}

func taskFromUploadSnapshot(upload UploadSnapshot) task.Task {
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
	}
	if upload.Extra != nil {
		if localPath, ok := upload.Extra["local_path"]; ok {
			detail["local_path"] = localPath
		}
	}
	return task.Task{
		ID:          upload.OpID,
		Type:        task.TypeUploadRemote,
		State:       state,
		Scope:       task.ScopeWriteback,
		Mount:       upload.Mount,
		Path:        upload.Path,
		Name:        upload.Name,
		BytesTotal:  upload.BytesTotal,
		BytesDone:   upload.BytesUploaded,
		StartedAt:   upload.StartedAt,
		UpdatedAt:   updatedAt,
		CompletedAt: upload.CompletedAt,
		RetryCount:  upload.RetryCount,
		LastError:   upload.LastError,
		NextAttempt: timeFromUnixNano(upload.NextAttemptAt),
		Cancelable:  state != task.StateSucceeded && state != task.StateCanceled,
		Retryable:   state == task.StateFailed || state == task.StateRetryWait,
		Detail:      compactTaskDetail(detail),
	}
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
