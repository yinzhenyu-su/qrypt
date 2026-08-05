package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"maps"
	"sort"
	"time"
)

func (v *VFS) uploadSnapshots(pending []PendingUpload) []UploadSnapshot {
	active := newVFSDebugUploadRuntime(v).ActiveSnapshots()

	timerPaths := newVFSUploadScheduler(v).TimerPaths()

	uploads := make([]UploadSnapshot, 0, len(pending)+len(active))
	seen := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			uploads = append(uploads, upload)
			seen[item.Path] = true
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
		uploads = append(uploads, UploadSnapshot{
			OpID:           item.FID,
			Path:           item.Path,
			Name:           item.Name,
			State:          state,
			BytesTotal:     item.Size,
			UpdatedAt:      timeFromUnixNano(item.UpdatedAt),
			RetryCount:     item.RetryCount,
			LastError:      item.LastError,
			LastAttemptAt:  item.LastAttemptAt,
			NextAttemptAt:  item.NextAttemptAt,
			ParentRemoteID: item.ParentID,
		})
		seen[item.Path] = true
	}
	for path, upload := range active {
		if !seen[path] {
			uploads = append(uploads, upload)
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].Path < uploads[j].Path
	})
	return uploads
}

func (v *VFS) uploadSnapshotHistory() []UploadSnapshot {
	return newVFSDebugUploadRuntime(v).History()
}

func (v *VFS) removeUploadHistoryByID(id string) bool {
	return newVFSDebugUploadRuntime(v).RemoveHistoryByID(id)
}

func (v *VFS) startUploadSnapshot(p PendingUpload) {
	newVFSDebugUploadRuntime(v).StartSnapshot(p)
}

func (v *VFS) setUploadSnapshotState(path, state string) {
	newVFSDebugUploadRuntime(v).SetSnapshotState(path, state)
}

func (v *VFS) finishUploadSnapshot(path, state, lastError string) {
	newVFSDebugUploadRuntime(v).FinishSnapshot(path, state, lastError)
}

func (v *VFS) setUploadSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	newVFSDebugUploadRuntime(v).SetSnapshotMetadata(path, resultRemoteID, hashes)
}

func (s *uploadSnapshotState) recordStageDuration(now time.Time) {
	if s.stageStartedAt.IsZero() || s.upload.State == "" {
		s.stageStartedAt = now
		return
	}
	if s.upload.StageDurations == nil {
		s.upload.StageDurations = map[string]string{}
	}
	if s.stageDurations == nil {
		s.stageDurations = map[string]time.Duration{}
	}
	s.stageDurations[s.upload.State] += now.Sub(s.stageStartedAt)
	s.upload.StageDurations[s.upload.State] = s.stageDurations[s.upload.State].String()
	s.stageStartedAt = now
}

func (v *VFS) updateUploadSnapshot(path string, n int) {
	newVFSDebugUploadRuntime(v).UpdateSnapshot(path, n)
}

func (v *VFS) recordUploadEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	newVFSDebugUploadRuntime(v).RecordEvent(path, phase, start, bytes, extra)
}

func (v *VFS) setUploadSnapshotExtra(path string, key string, value any) {
	newVFSDebugUploadRuntime(v).SetSnapshotExtra(path, key, value)
}

type vfsDebugUploadRuntime struct {
	v *VFS
}

func newVFSDebugUploadRuntime(v *VFS) vfsDebugUploadRuntime {
	return vfsDebugUploadRuntime{v: v}
}

func (r vfsDebugUploadRuntime) ActiveSnapshots() map[string]UploadSnapshot {
	active := map[string]UploadSnapshot{}
	r.v.uploadDebug.mu.Lock()
	for path, state := range r.v.uploadDebug.active {
		active[path] = cloneUploadSnapshot(state.upload)
	}
	r.v.uploadDebug.mu.Unlock()
	return active
}

func (r vfsDebugUploadRuntime) History() []UploadSnapshot {
	r.v.uploadDebug.mu.Lock()
	defer r.v.uploadDebug.mu.Unlock()
	out := make([]UploadSnapshot, len(r.v.uploadDebug.history))
	for i, upload := range r.v.uploadDebug.history {
		out[i] = cloneUploadSnapshot(upload)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

// cloneUploadSnapshot deep-copies the slice and map fields that are mutated
// under the upload debug lock, so snapshot consumers can read their copy
// outside the lock without racing the upload pipeline.
func cloneUploadSnapshot(u UploadSnapshot) UploadSnapshot {
	u.Events = append([]drive.MetricEvent(nil), u.Events...)
	u.Hashes = append([]string(nil), u.Hashes...)
	u.Extra = maps.Clone(u.Extra)
	u.StageDurations = maps.Clone(u.StageDurations)
	return u
}

func (r vfsDebugUploadRuntime) RemoveHistoryByID(id string) bool {
	if id == "" {
		return false
	}
	r.v.uploadDebug.mu.Lock()
	defer r.v.uploadDebug.mu.Unlock()
	for i, upload := range r.v.uploadDebug.history {
		if upload.OpID != id {
			continue
		}
		copy(r.v.uploadDebug.history[i:], r.v.uploadDebug.history[i+1:])
		r.v.uploadDebug.history = r.v.uploadDebug.history[:len(r.v.uploadDebug.history)-1]
		return true
	}
	return false
}

func (r vfsDebugUploadRuntime) StartSnapshot(p PendingUpload) {
	now := timeutil.Now()
	r.v.uploadDebug.mu.Lock()
	r.v.uploadDebug.active[p.Path] = &uploadSnapshotState{
		stageStartedAt: now,
		upload: UploadSnapshot{
			OpID:           p.FID,
			Path:           p.Path,
			Name:           p.Name,
			State:          "starting",
			BytesTotal:     p.Size,
			StartedAt:      now,
			UpdatedAt:      now,
			RetryCount:     p.RetryCount,
			LastError:      p.LastError,
			LastAttemptAt:  p.LastAttemptAt,
			NextAttemptAt:  p.NextAttemptAt,
			ParentRemoteID: p.ParentID,
		},
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotState(path, state string) {
	r.v.uploadDebug.mu.Lock()
	if upload := r.v.uploadDebug.active[path]; upload != nil {
		upload.recordStageDuration(timeutil.Now())
		upload.upload.State = state
		if state == string(drive.UploadPhaseInstant) {
			upload.upload.Instant = true
		}
		upload.upload.UpdatedAt = upload.stageStartedAt
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) FinishSnapshot(path, state, lastError string) {
	r.v.uploadDebug.mu.Lock()
	if upload := r.v.uploadDebug.active[path]; upload != nil {
		now := timeutil.Now()
		upload.recordStageDuration(now)
		upload.upload.State = state
		upload.upload.LastError = lastError
		if lastError != "" {
			upload.upload.ErrorCategory = drive.ErrorCategoryMessage(lastError)
		}
		upload.upload.UpdatedAt = now
		upload.upload.CompletedAt = upload.upload.UpdatedAt
		r.v.uploadDebug.history = append(r.v.uploadDebug.history, upload.upload)
		if len(r.v.uploadDebug.history) > uploadSnapshotHistoryLimit {
			copy(r.v.uploadDebug.history, r.v.uploadDebug.history[len(r.v.uploadDebug.history)-uploadSnapshotHistoryLimit:])
			r.v.uploadDebug.history = r.v.uploadDebug.history[:uploadSnapshotHistoryLimit]
		}
		delete(r.v.uploadDebug.active, path)
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	r.v.uploadDebug.mu.Lock()
	if state := r.v.uploadDebug.active[path]; state != nil {
		if resultRemoteID != "" {
			state.upload.ResultRemoteID = resultRemoteID
		}
		if len(hashes) > 0 {
			state.upload.Hashes = append([]string(nil), hashes...)
		}
		state.upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) UpdateSnapshot(path string, n int) {
	if n <= 0 {
		return
	}
	r.v.uploadDebug.mu.Lock()
	if state := r.v.uploadDebug.active[path]; state != nil {
		state.upload.BytesUploaded += int64(n)
		if state.upload.BytesTotal >= 0 && state.upload.BytesUploaded > state.upload.BytesTotal {
			state.upload.BytesUploaded = state.upload.BytesTotal
		}
		state.upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) RecordEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	if phase == "" || start.IsZero() {
		return
	}
	finished := timeutil.Now()
	duration := finished.Sub(start)
	event := drive.MetricEvent{
		At:         finished,
		Kind:       "vfs_upload",
		Operation:  "upload",
		Phase:      phase,
		State:      "completed",
		OK:         true,
		Bytes:      bytes,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
		StartedAt:  start,
		FinishedAt: finished,
		Extra:      extra,
	}
	if message, ok := extra["error"].(string); ok && message != "" {
		event.State = "failed"
		event.OK = false
		event.Error = message
		event.ErrorCategory = drive.ErrorCategoryMessage(message)
	}
	if bytes > 0 && duration > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	r.v.uploadDebug.mu.Lock()
	if state := r.v.uploadDebug.active[path]; state != nil {
		state.upload.Events = append(state.upload.Events, event)
	}
	r.v.uploadDebug.mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotExtra(path string, key string, value any) {
	if key == "" {
		return
	}
	r.v.uploadDebug.mu.Lock()
	if state := r.v.uploadDebug.active[path]; state != nil {
		if state.upload.Extra == nil {
			state.upload.Extra = map[string]any{}
		}
		state.upload.Extra[key] = value
		state.upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploadDebug.mu.Unlock()
}
