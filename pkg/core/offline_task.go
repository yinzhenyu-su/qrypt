package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// Offline task creation and recovery paths.
//
// Creation lives in offline.go; the provider-backed runner lives in
// offline_runner.go and offline_fetch.go. This file wires the two into the
// task descriptors' creation and recovery declarations and owns the
// retry/active-runner bookkeeping.

// beginOfflineRun registers a task's runner. It is the "no two active runners"
// guard shared by recovery and user-initiated retry.
func (c *Core) beginOfflineRun(taskID string) bool {
	c.offlineMu.Lock()
	defer c.offlineMu.Unlock()
	if c.offlineActive == nil {
		c.offlineActive = map[string]struct{}{}
	}
	if _, exists := c.offlineActive[taskID]; exists {
		return false
	}
	c.offlineActive[taskID] = struct{}{}
	return true
}

func (c *Core) endOfflineRun(taskID string) {
	c.offlineMu.Lock()
	defer c.offlineMu.Unlock()
	delete(c.offlineActive, taskID)
}

func (c *Core) offlineRunActive(taskID string) bool {
	c.offlineMu.Lock()
	defer c.offlineMu.Unlock()
	_, exists := c.offlineActive[taskID]
	return exists
}

// recoverOfflineDownloadTasks resumes provider observation for interrupted
// cloud-side offline tasks.
func (c *Core) recoverOfflineDownloadTasks(ctx context.Context, manager *task.Manager) {
	c.recoverOfflineTasks(ctx, manager, task.TypeOfflineDownload)
}

// recoverOfflineFetchTasks resumes an interrupted offline fetch from its
// persisted phase.
func (c *Core) recoverOfflineFetchTasks(ctx context.Context, manager *task.Manager) {
	c.recoverOfflineTasks(ctx, manager, task.TypeOfflineFetch)
}

func (c *Core) recoverOfflineTasks(ctx context.Context, manager *task.Manager, typ task.Type) {
	if c == nil || manager == nil || c.fs == nil {
		return
	}
	items, err := manager.ListTasks(ctx, task.Filter{Types: []task.Type{typ}})
	if err != nil {
		return
	}
	for _, item := range items {
		if !offlineRecoverable(item.State, item.Error) {
			continue
		}
		if c.offlineRunActive(item.ID) {
			continue
		}
		id, fetch := item.ID, item.Type == task.TypeOfflineFetch
		_, ok, err := manager.RecoverTask(ctx, id, func(runCtx context.Context, update task.UpdateFunc) error {
			return c.runOfflineTask(runCtx, update, id, fetch)
		})
		if err != nil || !ok {
			c.endOfflineRun(id)
		}
	}
}

// offlineRecoverable reports whether an interrupted or failed offline task
// should be resumed. Replay normalization turns an interrupted non-terminal
// task into a failed task with the "interrupted" code; both forms resume.
func offlineRecoverable(state task.State, taskError *task.Error) bool {
	if state == task.StateFailed && taskError != nil && taskError.Code == "interrupted" {
		return true
	}
	if offlineTaskTerminal(state) {
		return false
	}
	switch state {
	case task.StateQueued, task.StateScheduled, task.StateRunning, task.StateCanceling:
		return true
	default:
		return false
	}
}

// retryOfflineTask implements the offline branch of Core.RetryTask: a failed
// or canceled offline task is retried in place when the provider supports it,
// otherwise re-created from the persisted inputs.
func (c *Core) retryOfflineTask(ctx context.Context, current task.Task) (bool, error) {
	if current.Type != task.TypeOfflineDownload && current.Type != task.TypeOfflineFetch {
		return false, nil
	}
	if c.offlineRunActive(current.ID) {
		return true, fmt.Errorf("core: offline task %q is still stopping", current.ID)
	}
	switch current.State {
	case task.StateSucceeded, task.StatePartialFailed:
		return true, fmt.Errorf("core: offline task %q already succeeded; create a new task instead", current.ID)
	case task.StateFailed, task.StateCanceled:
	default:
		return true, fmt.Errorf("core: offline task %q cannot be retried in state %s", current.ID, current.State)
	}
	detail, ok := decodeOfflineDetail(current.Detail)
	if !ok {
		return true, fmt.Errorf("core: offline task %q has no offline detail", current.ID)
	}
	driver, ok := c.offlineDriver(detail.ParseMount)
	if !ok {
		return true, fmt.Errorf("%w: mount %q", ErrOfflineMountNotFound, detail.ParseMount)
	}

	// A staging retry needs the provider task re-queued or re-created. A
	// transfer/cleanup retry keeps the staging task and just re-runs the
	// transfer, so it does not touch the provider.
	if detail.Phase == "" || detail.Phase == offlinePhaseStaging || detail.Phase == offlinePhaseComplete {
		if detail.ProviderID != "" {
			if !retryProviderInPlace(ctx, driver, detail.ProviderID) {
				detail.PreviousProviderIDs = append(detail.PreviousProviderIDs, detail.ProviderID)
				detail.ProviderID = ""
			}
		}
		detail.Phase = offlinePhaseStaging
		detail.ResultFiles = nil
		detail.TransferDone = false
		detail.CleanupDone = false
	}

	manager, err := c.taskManager()
	if err != nil {
		return true, err
	}
	updated, ok := manager.UpdateTask(current.ID, func(item *task.Task) {
		detail.apply(item)
		item.Error = nil
		item.Capabilities.Retryable = false
		item.Capabilities.Cancelable = true
	})
	if !ok {
		return true, fmt.Errorf("core: offline task %q is not managed", current.ID)
	}
	fetch := updated.Type == task.TypeOfflineFetch
	_, recovered, err := manager.RecoverTask(ctx, current.ID, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runOfflineTask(runCtx, update, current.ID, fetch)
	})
	if err != nil {
		return true, err
	}
	if !recovered {
		return true, fmt.Errorf("core: offline task %q is not recoverable", current.ID)
	}
	return true, nil
}

// retryProviderInPlace asks a provider that supports it to re-queue its own
// task. A provider without the capability, or one that rejects the retry,
// falls back to re-creating the task.
func retryProviderInPlace(ctx context.Context, driver drive.Driver, providerID string) bool {
	retrier, ok := driver.(drive.OfflineRetrier)
	if !ok {
		return false
	}
	return retrier.RetryOfflineDownload(ctx, providerID) == nil
}
