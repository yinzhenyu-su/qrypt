package vfs

import (
	"context"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	idelete "github.com/yinzhenyu/qrypt/internal/vfs/delete"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) scheduleDelete(path string, entry drive.Entry) {
	if v.deletes.delay <= 0 {
		logging.L.Infof("[VFS] delete remote now path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
		v.deleteRemote(v.lifecycleContext(), path, entry)
		return
	}
	newVFSDeleteScheduler(v).Schedule(path, entry, v.deletes.delay, func() {
		logging.L.Infof("[VFS] delete timer fired path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
		v.deleteRemote(v.lifecycleContext(), path, entry)
	})
}

// lifecycleContext returns the VFS lifecycle context when Start has run, or
// context.Background() for the short window before Start (deletes scheduled
// during construction in tests). Background tasks derive from it so a VFS
// shutdown cancels in-flight remote operations.
func (v *VFS) lifecycleContext() context.Context {
	if v.ctx != nil {
		return v.ctx
	}
	return context.Background()
}
func (v *VFS) cancelChildDeletes(dir string) {
	newVFSDeleteScheduler(v).CancelChildren(dir)
}
func (v *VFS) deleteRemote(ctx context.Context, path string, entry drive.Entry) {
	idelete.NewExecutor(newVFSDeleteExecutorDeps(v)).Execute(ctx, path, entry)
}

// newVFSDeleteExecutorDeps adapts VFS internals to idelete.ExecutorDeps.
func newVFSDeleteExecutorDeps(v *VFS) idelete.ExecutorDeps {
	return idelete.ExecutorDeps{
		Driver:  newVFSDriverRuntime(v),
		Overlay: vfsDeleteOverlayOps{v: v},
		Health:  v.healthTracker,
		Upload:  vfsDeleteUploadCleanup{v: v},
	}
}

type vfsDeleteOverlayOps struct {
	v *VFS
}

func (r vfsDeleteOverlayOps) BeginDelete(path string, entryID string) bool {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	current, ok := r.v.view.overlay.deleted[path]
	if !ok || current.ID != entryID {
		return false
	}
	return true
}

func (r vfsDeleteOverlayOps) MarkDeleteActive(path string, entry drive.Entry) {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	r.v.deletes.tasks.active[path] = entry
	delete(r.v.deletes.tasks.failures, path)
}

func (r vfsDeleteOverlayOps) MarkDeleteFailed(path string, err error) {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	delete(r.v.deletes.tasks.active, path)
	if err != nil {
		r.v.deletes.tasks.failures[path] = err.Error()
	}
}

func (r vfsDeleteOverlayOps) MarkDeleteComplete(path string, entry drive.Entry) {
	r.v.view.overlay.mu.Lock()
	delete(r.v.deletes.tasks.active, path)
	delete(r.v.deletes.tasks.failures, path)
	delete(r.v.view.overlay.deleted, path)
	delete(r.v.view.overlay.restoredDirs, path)
	r.v.view.overlay.mu.Unlock()

	r.v.view.mu.Lock()
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsDeleteOverlayOps) CancelDelete(path string) {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	delete(r.v.deletes.tasks.active, path)
	delete(r.v.deletes.tasks.failures, path)
	r.v.deletes.tasks.scheduler.Cancel(path)
	delete(r.v.view.overlay.deleted, path)
}

type vfsDeleteUploadCleanup struct {
	v *VFS
}

func (r vfsDeleteUploadCleanup) RemoveUploadState(path string) {
	if err := r.v.uploads.Store().RemoveUpload(path); err == nil {
		r.v.hashes.removePath(path)
	}
}

type vfsDeleteScheduler struct {
	v *VFS
}

func newVFSDeleteScheduler(v *VFS) vfsDeleteScheduler {
	return vfsDeleteScheduler{v: v}
}

func (s vfsDeleteScheduler) Schedule(path string, entry drive.Entry, delay time.Duration, fire func()) {
	s.v.view.overlay.mu.Lock()
	delete(s.v.deletes.tasks.failures, path)
	s.v.view.overlay.mu.Unlock()
	s.v.deletes.tasks.scheduler.Schedule(path, delay, fire)
}

func (s vfsDeleteScheduler) CancelChildren(dir string) {
	dir = cleanVirtual(dir)
	removed := []string{}
	for path := range s.v.deletes.tasks.scheduler.Keys() {
		if isPathUnder(path, dir) {
			removed = append(removed, path)
		}
	}
	s.v.deletes.tasks.scheduler.CancelUnder(dir)
	s.v.view.overlay.mu.Lock()
	defer s.v.view.overlay.mu.Unlock()
	for _, path := range removed {
		delete(s.v.view.overlay.deleted, path)
		delete(s.v.deletes.tasks.failures, path)
	}
}

func (s vfsDeleteScheduler) StopAll() {
	s.v.deletes.tasks.stopAll()
}
