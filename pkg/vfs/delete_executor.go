package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"time"
)

func (v *VFS) scheduleDelete(path string, entry drive.Entry) {
	if v.delete.delay <= 0 {
		logging.L.Infof("[VFS] delete remote now path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
		v.deleteRemote(v.lifecycleContext(), path, entry)
		return
	}
	newVFSDeleteScheduler(v).Schedule(path, entry, v.delete.delay, func() {
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
	v.deleteRemoteWithRuntime(ctx, path, entry, newVFSDeleteRemoteRuntime(v))
}

func (v *VFS) deleteRemoteWithRuntime(ctx context.Context, path string, entry drive.Entry, runtime deleteRemoteRuntime) {
	if !runtime.BeginRemoteDelete(path, entry) {
		logging.L.Infof("[VFS] delete remote skipped path=%q id=%q", path, entry.ID)
		return
	}
	err := runtime.RemoveRemote(ctx, entry)
	runtime.RecordDeleteHealth(err)
	if err != nil {
		logging.L.Warnf("[VFS] delete remote failed path=%q id=%q dir=%t err=%v", path, entry.ID, entry.IsDir, err)
		runtime.MarkRemoteDeleteFailed(path, err)
		return
	}
	logging.L.Infof("[VFS] delete remote complete path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
	runtime.MarkRemoteDeleteComplete(path, entry)
	runtime.CleanupUploadState(path)
}
func (v *VFS) stopDeleteTimers() {
	newVFSDeleteScheduler(v).StopAll()
}

type deleteRemoteRuntime interface {
	BeginRemoteDelete(path string, entry drive.Entry) bool
	RemoveRemote(ctx context.Context, entry drive.Entry) error
	RecordDeleteHealth(err error)
	MarkRemoteDeleteFailed(path string, err error)
	MarkRemoteDeleteComplete(path string, entry drive.Entry)
	CleanupUploadState(path string)
}

type vfsDeleteRemoteRuntime struct {
	v *VFS
}

func newVFSDeleteRemoteRuntime(v *VFS) vfsDeleteRemoteRuntime {
	return vfsDeleteRemoteRuntime{v: v}
}

func (r vfsDeleteRemoteRuntime) BeginRemoteDelete(path string, entry drive.Entry) bool {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	current, ok := r.v.view.overlay.deleted[path]
	if !ok || current.ID != entry.ID {
		return false
	}
	r.v.delete.tasks.active[path] = entry
	delete(r.v.delete.tasks.failures, path)
	return true
}

func (r vfsDeleteRemoteRuntime) RemoveRemote(ctx context.Context, entry drive.Entry) error {
	return newVFSDriverRuntime(r.v).Remove(ctx, entry)
}

func (r vfsDeleteRemoteRuntime) RecordDeleteHealth(err error) {
	r.v.healthTracker.RecordResult(drive.HealthOpDelete, err)
}

func (r vfsDeleteRemoteRuntime) MarkRemoteDeleteFailed(path string, err error) {
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	delete(r.v.delete.tasks.active, path)
	if err != nil {
		r.v.delete.tasks.failures[path] = err.Error()
	}
}

func (r vfsDeleteRemoteRuntime) MarkRemoteDeleteComplete(path string, entry drive.Entry) {
	r.v.view.overlay.mu.Lock()
	delete(r.v.delete.tasks.active, path)
	delete(r.v.delete.tasks.failures, path)
	delete(r.v.view.overlay.deleted, path)
	delete(r.v.view.overlay.restoredDirs, path)
	r.v.view.overlay.mu.Unlock()

	r.v.view.mu.Lock()
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsDeleteRemoteRuntime) CleanupUploadState(path string) {
	if err := r.v.upload.store.RemoveUpload(path); err == nil {
		r.v.upload.hashes.removePath(path)
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
	defer s.v.view.overlay.mu.Unlock()
	delete(s.v.delete.tasks.failures, path)
	if timer := s.v.delete.tasks.timers[path]; timer != nil {
		timer.Stop()
	}
	s.v.delete.tasks.timers[path] = time.AfterFunc(delay, func() {
		s.v.view.overlay.mu.Lock()
		delete(s.v.delete.tasks.timers, path)
		s.v.view.overlay.mu.Unlock()
		fire()
	})
}

func (s vfsDeleteScheduler) CancelChildren(dir string) {
	dir = cleanVirtual(dir)
	s.v.view.overlay.mu.Lock()
	defer s.v.view.overlay.mu.Unlock()
	for path, timer := range s.v.delete.tasks.timers {
		if isPathUnder(path, dir) {
			timer.Stop()
			delete(s.v.delete.tasks.timers, path)
			delete(s.v.view.overlay.deleted, path)
			delete(s.v.delete.tasks.failures, path)
		}
	}
}

func (s vfsDeleteScheduler) StopAll() {
	s.v.view.overlay.mu.Lock()
	defer s.v.view.overlay.mu.Unlock()
	for path, timer := range s.v.delete.tasks.timers {
		timer.Stop()
		delete(s.v.delete.tasks.timers, path)
	}
}
