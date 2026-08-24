package delete

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// Executor performs the remote-delete step of a pending delete: it removes
// the entry on the driver, records health, and updates the overlay state.
// Overlay bookkeeping and upload-state cleanup are driven through
// ExecutorDeps so the executor stays free of VFS internals.
type Executor struct {
	deps ExecutorDeps
}

func NewExecutor(deps ExecutorDeps) *Executor {
	return &Executor{deps: deps}
}

func (e *Executor) Execute(ctx context.Context, path string, entry drive.Entry) {
	overlay := e.deps.Overlay
	if !overlay.BeginDelete(path, entry.ID) {
		logging.L.Infof("[VFS] delete remote skipped path=%q id=%q", path, entry.ID)
		return
	}
	overlay.MarkDeleteActive(path, entry)
	var err error
	if entry.IsDir && e.deps.WaitForDescendantDeletes != nil {
		err = e.deps.WaitForDescendantDeletes(ctx, path)
	}
	if err == nil {
		err = e.deps.Driver.Remove(ctx, entry)
	}
	e.deps.Health.RecordResult(drive.HealthOpDelete, err)
	if err != nil {
		logging.L.Warnf("[VFS] delete remote failed path=%q id=%q dir=%t err=%v", path, entry.ID, entry.IsDir, err)
		overlay.MarkDeleteFailed(path, err)
		return
	}
	logging.L.Infof("[VFS] delete remote complete path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
	overlay.MarkDeleteComplete(path, entry)
	e.deps.Upload.RemoveUploadState(path)
}
