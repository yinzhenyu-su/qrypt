package core

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core/ownerlock"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// SetShutdownHandler registers the callback invoked by a control shutdown
// request. The serve command installs it; a mount-hosted Core leaves it unset
// so `qrypt core stop` cannot tear down a user's mount by accident.
func (c *Core) SetShutdownHandler(handler func()) {
	if c == nil {
		return
	}
	c.shutdownMu.Lock()
	c.shutdownHandler = handler
	c.shutdownMu.Unlock()
}

// RequestShutdown asks the owning process to stop. It returns once the request
// is accepted; teardown happens asynchronously in the process loop.
func (c *Core) RequestShutdown() error {
	if c == nil {
		return fmt.Errorf("core: closed")
	}
	c.shutdownMu.Lock()
	handler := c.shutdownHandler
	c.shutdownMu.Unlock()
	if handler == nil {
		return fmt.Errorf("core: this runtime does not support remote shutdown")
	}
	go handler()
	return nil
}

// RuntimeStatus reports the identity and liveness of this Core. It is cheap
// enough for a status poll (one small config hash read).
func (c *Core) RuntimeStatus() control.RuntimeStatus {
	status := control.RuntimeStatus{PID: os.Getpid(), Version: buildinfo.Current().Version}
	if c == nil {
		return status
	}
	status.StartedAt = c.startedAt
	if !c.startedAt.IsZero() {
		status.UptimeSeconds = int64(time.Since(c.startedAt).Seconds())
	}
	status.ConfigPath = c.configPath
	status.WorkDir = c.runtimeLayout.RootDir
	if c.configPath != "" {
		if fingerprint, err := ownerlock.FingerprintFile(c.configPath); err == nil {
			status.Fingerprint = fingerprint
		}
	}
	status.Busy = c.Busy()
	return status
}

// Busy reports whether the Core has unfinished work: pending uploads or a task
// in a non-terminal state. The serve command treats a busy Core as active for
// idle-timeout purposes.
func (c *Core) Busy() bool {
	if c == nil || c.fs == nil {
		return false
	}
	if inspector, ok := c.fs.(vfs.UploadInspector); ok && len(inspector.PendingUploads()) > 0 {
		return true
	}
	c.tasksMu.Lock()
	tasks := c.tasks
	c.tasksMu.Unlock()
	if tasks == nil {
		return false
	}
	active, err := tasks.ListTasks(context.Background(), task.Filter{States: []task.State{
		task.StateQueued,
		task.StateScheduled,
		task.StateRunning,
		task.StateRetryWait,
		task.StateCanceling,
	}})
	if err != nil {
		return false
	}
	return len(active) > 0
}

// coreRuntimeService adapts a Core to the control endpoint's lifecycle
// surface.
type coreRuntimeService struct {
	core *Core
}

func (s coreRuntimeService) Status(context.Context) (control.RuntimeStatus, error) {
	if s.core == nil {
		return control.RuntimeStatus{}, fmt.Errorf("core: closed")
	}
	return s.core.RuntimeStatus(), nil
}

func (s coreRuntimeService) Shutdown(context.Context) error {
	return s.core.RequestShutdown()
}
