package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	idelete "github.com/yinzhenyu/qrypt/pkg/vfs/delete"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"time"
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
	if p := v.ctx.Load(); p != nil {
		return *p
	}
	return context.Background()
}

func (v *VFS) deleteRemote(ctx context.Context, path string, entry drive.Entry) {
	idelete.NewExecutor(newVFSDeleteExecutorDeps(v)).Execute(ctx, path, entry)
}

// newVFSDeleteExecutorDeps adapts VFS internals to idelete.ExecutorDeps.
func newVFSDeleteExecutorDeps(v *VFS) idelete.ExecutorDeps {
	return idelete.ExecutorDeps{
		Driver:                   newVFSDriverRuntime(v),
		Overlay:                  newVFSDeleteOverlayOps(v),
		Health:                   v.healthTracker,
		Upload:                   newVFSDeleteUploadCleanup(v.uploads.Store(), v.hashes),
		WaitForDescendantDeletes: v.waitForActiveChildDeletes,
	}
}

// vfsDeleteOverlayOps adapts the overlay + delete-task domain to
// idelete.OverlayOps. The two states share one lock (view.NewOverlayTasks),
// so every compound critical section runs in the view package; this adapter
// is a thin delegation over the exported sync surface.
type vfsDeleteOverlayOps struct {
	vis view.Visibility
}

func newVFSDeleteOverlayOps(v *VFS) vfsDeleteOverlayOps {
	return vfsDeleteOverlayOps{vis: view.NewVisibility(v.view.Overlay(), v.deletes.tasks, v.view, nil)}
}

func (r vfsDeleteOverlayOps) BeginDelete(path string, entryID string) bool {
	return r.vis.BeginDelete(path, entryID)
}

func (r vfsDeleteOverlayOps) MarkDeleteActive(path string, entry drive.Entry) {
	r.vis.MarkDeleteActive(path, entry)
}

func (r vfsDeleteOverlayOps) MarkDeleteFailed(path string, err error) {
	r.vis.MarkDeleteFailed(path, err)
}

func (r vfsDeleteOverlayOps) MarkDeleteComplete(path string, entry drive.Entry) {
	r.vis.MarkDeleteComplete(path, entry)
}

func (r vfsDeleteOverlayOps) CancelDelete(path string) {
	r.vis.CancelDelete(path)
}

type vfsDeleteUploadCleanup struct {
	store  *uploadStore
	hashes *upload.HashTracker
}

func newVFSDeleteUploadCleanup(store *uploadStore, hashes *upload.HashTracker) vfsDeleteUploadCleanup {
	return vfsDeleteUploadCleanup{store: store, hashes: hashes}
}

func (r vfsDeleteUploadCleanup) RemoveUploadState(path string) {
	if err := r.store.RemoveUpload(path); err == nil {
		r.hashes.RemovePath(path)
	}
}

type vfsDeleteScheduler struct {
	vis   view.Visibility
	tasks *view.Tasks
}

func newVFSDeleteScheduler(v *VFS) vfsDeleteScheduler {
	return vfsDeleteScheduler{
		vis:   view.NewVisibility(v.view.Overlay(), v.deletes.tasks, v.view, nil),
		tasks: v.deletes.tasks,
	}
}

func (s vfsDeleteScheduler) Schedule(path string, entry drive.Entry, delay time.Duration, fire func()) {
	s.vis.ScheduleDelete(path, delay, fire)
}

func (s vfsDeleteScheduler) TakeoverDirectory(dir string) {
	s.vis.TakeoverDirectory(dir)
}

func (s vfsDeleteScheduler) CancelChildren(dir string) {
	s.vis.CancelChildren(dir)
}

func (s vfsDeleteScheduler) StopAll() {
	s.tasks.StopAll()
}

func (v *VFS) waitForActiveChildDeletes(ctx context.Context, dir string) error {
	return v.deletes.tasks.WaitActiveChildren(ctx, dir)
}
