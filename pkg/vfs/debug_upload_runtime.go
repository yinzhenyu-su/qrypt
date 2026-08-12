package vfs

import (
	"maps"
	"sort"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (v *VFS) uploadSnapshots(pending []PendingUpload) []uploadSnapshot {
	active := newVFSDebugUploadRuntime(v).ActiveSnapshots()

	timerPaths := v.uploads.ScheduledDeadlines()

	uploads := make([]uploadSnapshot, 0, len(pending)+len(active))
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
		} else if _, ok := timerPaths[item.Path]; ok {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > util.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, uploadSnapshot{
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

func (v *VFS) uploadSnapshotHistory() []uploadSnapshot {
	return newVFSDebugUploadRuntime(v).History()
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

func (r vfsDebugUploadRuntime) ActiveSnapshots() map[string]uploadSnapshot {
	active := map[string]uploadSnapshot{}
	r.v.uploads.DebugState().Mu.Lock()
	for path, state := range r.v.uploads.DebugState().Active {
		active[path] = cloneUploadSnapshot(state.Upload)
	}
	r.v.uploads.DebugState().Mu.Unlock()
	return active
}

func (r vfsDebugUploadRuntime) History() []uploadSnapshot {
	r.v.uploads.DebugState().Mu.Lock()
	defer r.v.uploads.DebugState().Mu.Unlock()
	out := make([]uploadSnapshot, len(r.v.uploads.DebugState().History))
	for i, upload := range r.v.uploads.DebugState().History {
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
func cloneUploadSnapshot(u uploadSnapshot) uploadSnapshot {
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
	r.v.uploads.DebugState().Mu.Lock()
	defer r.v.uploads.DebugState().Mu.Unlock()
	for i, upload := range r.v.uploads.DebugState().History {
		if upload.OpID != id {
			continue
		}
		copy(r.v.uploads.DebugState().History[i:], r.v.uploads.DebugState().History[i+1:])
		r.v.uploads.DebugState().History = r.v.uploads.DebugState().History[:len(r.v.uploads.DebugState().History)-1]
		return true
	}
	return false
}

func (r vfsDebugUploadRuntime) StartSnapshot(p PendingUpload) {
	now := util.Now()
	r.v.uploads.DebugState().Mu.Lock()
	r.v.uploads.DebugState().Active[p.Path] = &uploadSnapshotState{
		StageStartedAt: now,
		Upload: uploadSnapshot{
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
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotState(path, state string) {
	r.v.uploads.DebugState().Mu.Lock()
	if upload := r.v.uploads.DebugState().Active[path]; upload != nil {
		upload.RecordStageDuration(util.Now())
		upload.Upload.State = state
		if state == string(drive.UploadPhaseInstant) {
			upload.Upload.Instant = true
		}
		upload.Upload.UpdatedAt = upload.StageStartedAt
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) FinishSnapshot(path, state, lastError string) {
	r.v.uploads.DebugState().Mu.Lock()
	if upload := r.v.uploads.DebugState().Active[path]; upload != nil {
		now := util.Now()
		upload.RecordStageDuration(now)
		upload.Upload.State = state
		upload.Upload.LastError = lastError
		if lastError != "" {
			upload.Upload.ErrorCategory = drive.ErrorCategoryMessage(lastError)
		}
		upload.Upload.UpdatedAt = now
		upload.Upload.CompletedAt = upload.Upload.UpdatedAt
		r.v.uploads.DebugState().History = append(r.v.uploads.DebugState().History, upload.Upload)
		if len(r.v.uploads.DebugState().History) > uploadSnapshotHistoryLimit {
			copy(r.v.uploads.DebugState().History, r.v.uploads.DebugState().History[len(r.v.uploads.DebugState().History)-uploadSnapshotHistoryLimit:])
			r.v.uploads.DebugState().History = r.v.uploads.DebugState().History[:uploadSnapshotHistoryLimit]
		}
		delete(r.v.uploads.DebugState().Active, path)
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		if resultRemoteID != "" {
			state.Upload.ResultRemoteID = resultRemoteID
		}
		if len(hashes) > 0 {
			state.Upload.Hashes = append([]string(nil), hashes...)
		}
		state.Upload.UpdatedAt = util.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) UpdateSnapshot(path string, n int) {
	if n <= 0 {
		return
	}
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		state.Upload.BytesUploaded += int64(n)
		if state.Upload.BytesTotal >= 0 && state.Upload.BytesUploaded > state.Upload.BytesTotal {
			state.Upload.BytesUploaded = state.Upload.BytesTotal
		}
		state.Upload.UpdatedAt = util.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) RecordEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	if phase == "" || start.IsZero() {
		return
	}
	finished := util.Now()
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
		DurationMS: diagnostics.DurationMillis(duration),
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
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		state.Upload.Events = append(state.Upload.Events, event)
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotExtra(path string, key string, value any) {
	if key == "" {
		return
	}
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		if state.Upload.Extra == nil {
			state.Upload.Extra = map[string]any{}
		}
		state.Upload.Extra[key] = value
		state.Upload.UpdatedAt = util.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}
