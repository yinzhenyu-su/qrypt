package vfs

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const DebugSnapshotSchemaVersion = 1
const debugUploadHistoryLimit = 100
const debugOpLimit = 500

type DebugSnapshot struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Kind          string               `json:"kind"`
	Mounts        []DebugMountSnapshot `json:"mounts"`
}

type DebugMountSnapshot struct {
	Name              string               `json:"name"`
	Pending           []PendingFile        `json:"pending"`
	Uploads           []DebugUpload        `json:"uploads"`
	UploadHistory     []DebugUpload        `json:"-"`
	Ops               []DebugOp            `json:"-"`
	Driver            *drive.DebugSnapshot `json:"driver,omitempty"`
	UploadQueueLength int                  `json:"upload_queue_length"`
	UploadQueueCap    int                  `json:"upload_queue_cap"`
	UploadWorkers     int                  `json:"upload_workers"`
	UploadDelay       string               `json:"upload_delay"`
	DeleteDelay       string               `json:"delete_delay"`
	UploadTimers      []DebugTimer         `json:"upload_timers"`
	DeleteTimers      []DebugTimer         `json:"delete_timers"`
	Deleted           []DebugDeletedEntry  `json:"deleted"`
	OverlayOps        []DebugOverlayOp     `json:"overlay_ops"`
	RestoredDirs      []DebugTimer         `json:"restored_dirs"`
	CopyHidden        []DebugCopyHidden    `json:"copy_hidden"`
	ReadCache         DebugReadCache       `json:"read_cache"`
	ActiveChunkLoads  int                  `json:"active_chunk_loads"`
	ActiveWindowLoads int                  `json:"active_window_loads"`
	ActivePrefetches  int                  `json:"active_prefetches"`
}

type DebugUpload struct {
	OpID          string         `json:"op_id"`
	Path          string         `json:"path"`
	Name          string         `json:"name"`
	State         string         `json:"state"`
	BytesTotal    int64          `json:"bytes_total"`
	BytesUploaded int64          `json:"bytes_uploaded"`
	StartedAt     time.Time      `json:"started_at,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at,omitempty"`
	RetryCount    int            `json:"retry_count"`
	LastError     string         `json:"last_error,omitempty"`
	LastAttemptAt int64          `json:"last_attempt_at,omitempty"`
	NextAttemptAt int64          `json:"next_attempt_at,omitempty"`
	CompletedAt   time.Time      `json:"completed_at,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

type DebugOp struct {
	ID      uint64         `json:"id"`
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	Path    string         `json:"path"`
	OpID    string         `json:"op_id,omitempty"`
	State   string         `json:"state,omitempty"`
	Message string         `json:"message,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

type debugUploadState struct {
	upload DebugUpload
}

type DebugTimer struct {
	Path     string    `json:"path"`
	Deadline time.Time `json:"deadline,omitempty"`
}

type DebugDeletedEntry struct {
	Path  string `json:"path"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type DebugOverlayOp struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	EntryID string `json:"entry_id"`
	IsDir   bool   `json:"is_dir"`
	OldGone bool   `json:"old_gone"`
	NewSeen bool   `json:"new_seen"`
}

type DebugCopyHidden struct {
	Dir   string       `json:"dir"`
	Names []DebugTimer `json:"names"`
}

type DebugReadCache struct {
	MaxBytes   int64                `json:"max_bytes"`
	ChunkCount int                  `json:"chunk_count"`
	Bytes      int64                `json:"bytes"`
	FileCount  int                  `json:"file_count"`
	Hits       int64                `json:"hits"`
	Misses     int64                `json:"misses"`
	Puts       int64                `json:"puts"`
	Evicted    int64                `json:"evicted"`
	Files      []DebugReadCacheFile `json:"files,omitempty"`
}

type DebugReadCacheFile struct {
	ID         string `json:"id"`
	ChunkCount int    `json:"chunk_count"`
	Bytes      int64  `json:"bytes"`
}

type DebugResolveInfo struct {
	Path       string `json:"path"`
	Parent     string `json:"parent"`
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name,omitempty"`
	RemoteID   string `json:"remote_id,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Pending    bool   `json:"pending"`
}

type DebugTask struct {
	Type      string    `json:"type"`
	Path      string    `json:"path"`
	State     string    `json:"state"`
	OpID      string    `json:"op_id,omitempty"`
	Deadline  time.Time `json:"deadline,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

type ConsistencyReport struct {
	Path             string `json:"path"`
	Parent           string `json:"parent"`
	Name             string `json:"name"`
	Pending          bool   `json:"pending"`
	RemoteFound      bool   `json:"remote_found"`
	RemoteID         string `json:"remote_id,omitempty"`
	RemoteSize       int64  `json:"remote_size,omitempty"`
	ExpectedSize     int64  `json:"expected_size,omitempty"`
	SizeMatches      bool   `json:"size_matches"`
	UploadInProgress bool   `json:"upload_in_progress"`
	Status           string `json:"status"`
	Issue            string `json:"issue,omitempty"`
}

func (v *VFS) DebugSnapshot() DebugSnapshot {
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Kind:          "vfs",
		Mounts:        []DebugMountSnapshot{v.debugMountSnapshot("default")},
	}
}

func (v *VFS) debugMountSnapshot(name string) DebugMountSnapshot {
	snapshot := DebugMountSnapshot{
		Name:              name,
		Pending:           v.cache.Pending(),
		UploadQueueLength: len(v.queue),
		UploadQueueCap:    cap(v.queue),
		UploadWorkers:     v.uploadWorkers,
		UploadDelay:       v.uploadDelay.String(),
		DeleteDelay:       v.deleteDelay.String(),
		ReadCache:         v.cache.debugReadCache(),
	}
	snapshot.Uploads = v.debugUploads(snapshot.Pending)
	snapshot.UploadHistory = v.debugUploadHistory()
	snapshot.Ops = v.DebugOps()
	if debugger, ok := v.driver.(drive.Debugger); ok {
		if driverSnapshot, err := debugger.DebugSnapshot(context.Background()); err == nil {
			snapshot.Driver = &driverSnapshot
		}
	}

	v.uploadMu.Lock()
	for path := range v.uploadTimers {
		snapshot.UploadTimers = append(snapshot.UploadTimers, DebugTimer{Path: path})
	}
	v.uploadMu.Unlock()
	sort.Slice(snapshot.UploadTimers, func(i, j int) bool {
		return snapshot.UploadTimers[i].Path < snapshot.UploadTimers[j].Path
	})

	now := time.Now()
	v.deleteMu.Lock()
	for path := range v.deleteTimers {
		snapshot.DeleteTimers = append(snapshot.DeleteTimers, DebugTimer{Path: path})
	}
	for path, entry := range v.deleted {
		snapshot.Deleted = append(snapshot.Deleted, DebugDeletedEntry{
			Path:  path,
			ID:    entry.ID,
			Name:  entry.Name,
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	for _, op := range v.overlayOps {
		snapshot.OverlayOps = append(snapshot.OverlayOps, DebugOverlayOp{
			OldPath: op.oldPath,
			NewPath: op.newPath,
			EntryID: op.entryID,
			IsDir:   op.isDir,
			OldGone: op.oldGone,
			NewSeen: op.newSeen,
		})
	}
	for path, deadline := range v.restoredDirs {
		if now.After(deadline) {
			continue
		}
		snapshot.RestoredDirs = append(snapshot.RestoredDirs, DebugTimer{Path: path, Deadline: deadline})
	}
	for dir, names := range v.copyHidden {
		item := DebugCopyHidden{Dir: dir}
		for name, deadline := range names {
			if now.After(deadline) {
				continue
			}
			item.Names = append(item.Names, DebugTimer{Path: name, Deadline: deadline})
		}
		sort.Slice(item.Names, func(i, j int) bool {
			return item.Names[i].Path < item.Names[j].Path
		})
		if len(item.Names) > 0 {
			snapshot.CopyHidden = append(snapshot.CopyHidden, item)
		}
	}
	v.deleteMu.Unlock()

	sort.Slice(snapshot.DeleteTimers, func(i, j int) bool {
		return snapshot.DeleteTimers[i].Path < snapshot.DeleteTimers[j].Path
	})
	sort.Slice(snapshot.Deleted, func(i, j int) bool {
		return snapshot.Deleted[i].Path < snapshot.Deleted[j].Path
	})
	sort.Slice(snapshot.OverlayOps, func(i, j int) bool {
		return snapshot.OverlayOps[i].OldPath < snapshot.OverlayOps[j].OldPath
	})
	sort.Slice(snapshot.RestoredDirs, func(i, j int) bool {
		return snapshot.RestoredDirs[i].Path < snapshot.RestoredDirs[j].Path
	})
	sort.Slice(snapshot.CopyHidden, func(i, j int) bool {
		return snapshot.CopyHidden[i].Dir < snapshot.CopyHidden[j].Dir
	})

	v.chunkLoadMu.Lock()
	snapshot.ActiveChunkLoads = len(v.chunkLoads)
	v.chunkLoadMu.Unlock()
	v.windowLoadMu.Lock()
	snapshot.ActiveWindowLoads = len(v.windowLoads)
	v.windowLoadMu.Unlock()
	v.prefetchMu.Lock()
	snapshot.ActivePrefetches = len(v.prefetching)
	v.prefetchMu.Unlock()

	return snapshot
}

func (v *VFS) recordOp(op DebugOp) {
	op.Time = time.Now()
	v.opMu.Lock()
	v.nextOpID++
	op.ID = v.nextOpID
	v.ops = append(v.ops, op)
	if len(v.ops) > debugOpLimit {
		copy(v.ops, v.ops[len(v.ops)-debugOpLimit:])
		v.ops = v.ops[:debugOpLimit]
	}
	v.opMu.Unlock()
}

func (v *VFS) DebugOps() []DebugOp {
	v.opMu.Lock()
	defer v.opMu.Unlock()
	return append([]DebugOp(nil), v.ops...)
}

func (v *VFS) DebugDriverHealth(ctx context.Context) map[string]drive.HealthStatus {
	if checker, ok := v.driver.(drive.HealthChecker); ok {
		return map[string]drive.HealthStatus{"default": checker.HealthCheck(ctx)}
	}
	return nil
}

func (v *VFS) debugUploads(pending []PendingFile) []DebugUpload {
	active := map[string]DebugUpload{}
	v.uploadMu.Lock()
	for path, state := range v.activeUploads {
		active[path] = state.upload
	}
	timerPaths := map[string]bool{}
	for path := range v.uploadTimers {
		timerPaths[path] = true
	}
	v.uploadMu.Unlock()

	uploads := make([]DebugUpload, 0, len(pending)+len(active))
	seen := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			uploads = append(uploads, upload)
			seen[item.Path] = true
			continue
		}
		state := "queued"
		if timerPaths[item.Path] {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > time.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, DebugUpload{
			OpID:          item.FID,
			Path:          item.Path,
			Name:          item.Name,
			State:         state,
			BytesTotal:    item.Size,
			RetryCount:    item.RetryCount,
			LastError:     item.LastError,
			LastAttemptAt: item.LastAttemptAt,
			NextAttemptAt: item.NextAttemptAt,
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

func (v *VFS) debugUploadHistory() []DebugUpload {
	v.uploadMu.Lock()
	defer v.uploadMu.Unlock()
	out := append([]DebugUpload(nil), v.uploadHistory...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

func (v *VFS) startDebugUpload(p PendingFile) {
	now := time.Now()
	v.uploadMu.Lock()
	v.activeUploads[p.Path] = &debugUploadState{upload: DebugUpload{
		OpID:          p.FID,
		Path:          p.Path,
		Name:          p.Name,
		State:         "uploading",
		BytesTotal:    p.Size,
		StartedAt:     now,
		UpdatedAt:     now,
		RetryCount:    p.RetryCount,
		LastError:     p.LastError,
		LastAttemptAt: p.LastAttemptAt,
		NextAttemptAt: p.NextAttemptAt,
	}}
	v.uploadMu.Unlock()
}

func (v *VFS) setDebugUploadState(path, state string) {
	v.uploadMu.Lock()
	if upload := v.activeUploads[path]; upload != nil {
		upload.upload.State = state
		upload.upload.UpdatedAt = time.Now()
	}
	v.uploadMu.Unlock()
}

func (v *VFS) finishDebugUpload(path, state, lastError string) {
	v.uploadMu.Lock()
	if upload := v.activeUploads[path]; upload != nil {
		upload.upload.State = state
		upload.upload.LastError = lastError
		upload.upload.UpdatedAt = time.Now()
		upload.upload.CompletedAt = upload.upload.UpdatedAt
		v.uploadHistory = append(v.uploadHistory, upload.upload)
		if len(v.uploadHistory) > debugUploadHistoryLimit {
			copy(v.uploadHistory, v.uploadHistory[len(v.uploadHistory)-debugUploadHistoryLimit:])
			v.uploadHistory = v.uploadHistory[:debugUploadHistoryLimit]
		}
		delete(v.activeUploads, path)
	}
	v.uploadMu.Unlock()
}

func (v *VFS) updateDebugUpload(path string, n int) {
	if n <= 0 {
		return
	}
	v.uploadMu.Lock()
	if state := v.activeUploads[path]; state != nil {
		state.upload.BytesUploaded += int64(n)
		state.upload.UpdatedAt = time.Now()
	}
	v.uploadMu.Unlock()
}

type uploadProgressReader struct {
	reader io.Reader
	update func(int)
}

func (r *uploadProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.update(n)
	return n, err
}

func (c *Cache) debugReadCache() DebugReadCache {
	snapshot := DebugReadCache{MaxBytes: c.maxSize}
	c.mu.RLock()
	snapshot.FileCount = len(c.chunks)
	snapshot.Hits = c.stats.hits
	snapshot.Misses = c.stats.misses
	snapshot.Puts = c.stats.puts
	snapshot.Evicted = c.stats.evicted
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		file := DebugReadCacheFile{ID: fid}
		for _, chunk := range fc.chunks {
			snapshot.ChunkCount++
			snapshot.Bytes += chunk.size
			file.ChunkCount++
			file.Bytes += chunk.size
		}
		if file.ChunkCount > 0 {
			snapshot.Files = append(snapshot.Files, file)
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	sort.Slice(snapshot.Files, func(i, j int) bool {
		return snapshot.Files[i].ID < snapshot.Files[j].ID
	})
	return snapshot
}

func (v *VFS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	path = cleanVirtual(path)
	info := DebugResolveInfo{
		Path:      path,
		Parent:    filepath.Dir(path),
		PlainName: filepath.Base(path),
	}
	if pending, err := v.pending(path); err == nil {
		info.Pending = true
		info.ParentID = pending.ParentID
		info.RemoteID = pending.FID
		info.Size = pending.Size
	}
	if entry, err := v.resolve(ctx, path); err == nil {
		info.RemoteID = entry.ID
		info.ParentID = entry.ParentID
		info.IsDir = entry.IsDir
		info.Size = entry.Size
	} else if !info.Pending {
		return DebugResolveInfo{}, err
	}
	if includeRemoteName {
		if resolver, ok := v.driver.(drive.RemoteNameResolver); ok {
			nameInfo, err := resolver.ResolveRemoteName(ctx, info.PlainName)
			if err != nil {
				return DebugResolveInfo{}, err
			}
			info.RemoteName = nameInfo.RemoteName
		} else {
			info.RemoteName = info.PlainName
		}
	}
	return info, nil
}

func (v *VFS) DebugTasks() []DebugTask {
	var tasks []DebugTask
	snapshot := v.debugMountSnapshot("")
	for _, upload := range snapshot.Uploads {
		tasks = append(tasks, DebugTask{
			Type:      "upload",
			Path:      upload.Path,
			State:     upload.State,
			OpID:      upload.OpID,
			UpdatedAt: upload.UpdatedAt,
			Detail:    fmt.Sprintf("%d/%d", upload.BytesUploaded, upload.BytesTotal),
		})
	}
	for _, timer := range snapshot.UploadTimers {
		tasks = append(tasks, DebugTask{Type: "upload_timer", Path: timer.Path, State: "scheduled"})
	}
	for _, timer := range snapshot.DeleteTimers {
		tasks = append(tasks, DebugTask{Type: "delete_timer", Path: timer.Path, State: "scheduled"})
	}
	for _, deleted := range snapshot.Deleted {
		tasks = append(tasks, DebugTask{Type: "delete", Path: deleted.Path, State: "pending", Detail: deleted.ID})
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Type == tasks[j].Type {
			return tasks[i].Path < tasks[j].Path
		}
		return tasks[i].Type < tasks[j].Type
	})
	return tasks
}

func (v *VFS) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	path = cleanVirtual(path)
	report := ConsistencyReport{Path: path, Parent: filepath.Dir(path), Name: filepath.Base(path)}
	expectedKnown := false
	if pending, err := v.pending(path); err == nil {
		report.Pending = true
		report.ExpectedSize = pending.Size
		expectedKnown = true
	}
	parent, err := v.resolve(ctx, report.Parent)
	if err != nil {
		report.Status = "error"
		report.Issue = err.Error()
		return report, nil
	}
	entries, err := v.driver.List(ctx, parent.ID)
	if err != nil {
		return ConsistencyReport{}, err
	}
	for _, entry := range entries {
		if entry.Name == report.Name {
			report.RemoteFound = true
			report.RemoteID = entry.ID
			report.RemoteSize = entry.Size
			if !expectedKnown {
				report.ExpectedSize = entry.Size
			}
			report.SizeMatches = entry.Size == report.ExpectedSize
			break
		}
	}
	for _, upload := range v.debugUploads(v.cache.Pending()) {
		if upload.Path == path && upload.State == "uploading" {
			report.UploadInProgress = true
			break
		}
	}
	switch {
	case report.Pending && report.RemoteFound && report.SizeMatches:
		report.Status = "uploaded_pending_cleanup"
	case report.Pending && report.RemoteFound && !report.SizeMatches:
		report.Status = "mismatch"
		report.Issue = "remote size differs from pending size"
	case report.Pending && !report.RemoteFound:
		report.Status = "pending"
	case !report.Pending && report.RemoteFound:
		report.Status = "ok"
		report.SizeMatches = true
	default:
		report.Status = "missing"
		report.Issue = "not pending and not found remotely"
	}
	return report, nil
}

func (n *Namespace) DebugSnapshot() DebugSnapshot {
	snapshot := DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Kind:          "namespace",
	}
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		snapshot.Mounts = append(snapshot.Mounts, n.mounts[name].debugMountSnapshot(name))
	}
	n.mu.RUnlock()
	return snapshot
}

func (n *Namespace) DebugDriverHealth(ctx context.Context) map[string]drive.HealthStatus {
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]drive.HealthStatus{}
	for _, name := range names {
		for _, status := range n.mounts[name].DebugDriverHealth(ctx) {
			out[name] = status
		}
	}
	n.mu.RUnlock()
	return out
}

func (n *Namespace) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	if root {
		return DebugResolveInfo{Path: "/", Parent: "/", PlainName: "/", IsDir: true}, nil
	}
	info, err := mount.DebugResolve(ctx, rest, includeRemoteName)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	info.Path = joinVirtual("/"+mountName, strings.TrimPrefix(info.Path, "/"))
	info.Parent = filepath.Dir(info.Path)
	return info, nil
}

func (n *Namespace) DebugTasks() []DebugTask {
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	var tasks []DebugTask
	for _, name := range names {
		for _, task := range n.mounts[name].DebugTasks() {
			task.Path = joinVirtual("/"+name, strings.TrimPrefix(task.Path, "/"))
			tasks = append(tasks, task)
		}
	}
	n.mu.RUnlock()
	return tasks
}

func (n *Namespace) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return ConsistencyReport{}, err
	}
	if root {
		return ConsistencyReport{Path: "/", Status: "namespace_root"}, nil
	}
	report, err := mount.DebugConsistency(ctx, rest)
	if err != nil {
		return ConsistencyReport{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	report.Path = joinVirtual("/"+mountName, strings.TrimPrefix(report.Path, "/"))
	report.Parent = filepath.Dir(report.Path)
	return report, nil
}
