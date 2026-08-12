package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

var UploadStreamTaskPollInterval = 500 * time.Millisecond

type uploadStreamBatch struct {
	mu             sync.Mutex
	taskID         string
	items          []*uploadStreamItem
	byID           map[string]*uploadStreamItem
	bytesTotal     int64
	conflictPolicy string
	update         task.UpdateFunc
	ready          chan struct{}
	readyOnce      sync.Once
	done           chan struct{}
	doneOnce       sync.Once
	canceled       bool
}

type uploadStreamItem struct {
	ID           string
	DestPath     string
	Name         string
	RelativePath string
	Size         int64
	Written      int64
	CloudWritten int64
	CloudTotal   int64
	CloudPhase   string
	RemoteID     string
	Open         bool
	State        task.State
	Error        *task.Error
}

type UploadStreamItemHandle struct {
	mu     sync.Mutex
	core   *Core
	batch  *uploadStreamBatch
	itemID string
	closed bool
}

func (c *Core) createUploadStreamTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	batch, err := c.uploadStreamBatchFromRequest(ctx, req)
	if err != nil {
		return task.Task{}, err
	}
	now := util.Now()
	first := batch.items[0]
	item := task.Task{
		ID:        batch.taskID,
		Type:      task.TypeUploadStreamBatch,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.DestPath,
		Name:      first.Name,
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal:        int64(len(batch.items)),
			StagingBytesTotal: batch.bytesTotal,
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  true,
			Dismissible: true,
		},
		Detail: map[string]any{
			"items":           batch.detailItems(),
			"conflict_policy": batch.conflictPolicy,
			"phase":           string(task.StateWaitingInput),
		},
		Result: task.Result{Items: batch.resultItemsLocked()},
	}
	destMount, _, _ := moveMounts(first.DestPath, first.DestPath, c.fs)
	if destMount != "" {
		item.Mount = destMount
		item.Detail["dest_mount"] = destMount
	}
	c.putUploadStream(batch)
	manager, err := c.taskManager()
	if err != nil {
		c.removeUploadStream(batch.taskID)
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runUploadStreamTask(runCtx, update, batch)
	}), nil
}

func (c *Core) runUploadStreamTask(ctx context.Context, update task.UpdateFunc, batch *uploadStreamBatch) error {
	batch.mu.Lock()
	batch.update = update
	batch.readyOnce.Do(func() { close(batch.ready) })
	batch.mu.Unlock()
	batch.updateTaskSnapshot()
	ticker := time.NewTicker(UploadStreamTaskPollInterval)
	defer ticker.Stop()
	defer c.removeUploadStream(batch.taskID)
	for {
		select {
		case <-ctx.Done():
			batch.markCanceled(ctx.Err())
			for _, item := range batch.itemsSnapshot() {
				if item.State != task.StateSucceeded {
					_ = c.cancelStreamingUpload(context.Background(), item.DestPath)
				}
			}
			batch.updateTaskSnapshot()
			return ctx.Err()
		case <-batch.done:
			return batch.finishTask(update)
		case <-ticker.C:
			c.refreshUploadStreamCloudProgress(ctx, batch)
			batch.updateTaskSnapshot()
		}
	}
}

func (c *Core) OpenUploadStreamItem(ctx context.Context, taskID, itemID string) (*UploadStreamItemHandle, error) {
	batch := c.getUploadStream(taskID)
	if batch == nil {
		return nil, fmt.Errorf("core: upload stream task %q not active", taskID)
	}
	select {
	case <-batch.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	batch.mu.Lock()
	if batch.canceled {
		batch.mu.Unlock()
		return nil, fmt.Errorf("core: upload stream task %q is canceled", taskID)
	}
	item := batch.byID[itemID]
	if item == nil {
		batch.mu.Unlock()
		return nil, fmt.Errorf("core: upload stream item %q not found", itemID)
	}
	if item.Open {
		batch.mu.Unlock()
		return nil, fmt.Errorf("core: upload stream item %q is already open", itemID)
	}
	if item.State == task.StateSucceeded {
		batch.mu.Unlock()
		return nil, fmt.Errorf("core: upload stream item %q already succeeded", itemID)
	}
	if item.State == task.StateCanceled || item.State == task.StateFailed {
		batch.mu.Unlock()
		return nil, fmt.Errorf("core: upload stream item %q is %s", itemID, item.State)
	}
	needsCreate := item.Written == 0
	destPath := item.DestPath
	item.Open = true
	item.State = task.StateRunning
	item.Error = nil
	batch.updateTaskSnapshotLocked()
	batch.mu.Unlock()
	if needsCreate {
		if err := c.beginStreamingUpload(ctx, destPath); err != nil {
			batch.mu.Lock()
			if current := batch.byID[itemID]; current != nil {
				current.Open = false
				current.State = task.StateWaitingInput
				current.Error = &task.Error{Message: err.Error(), Retryable: true}
			}
			batch.updateTaskSnapshotLocked()
			batch.mu.Unlock()
			return nil, err
		}
	}
	return &UploadStreamItemHandle{core: c, batch: batch, itemID: itemID}, nil
}

func (h *UploadStreamItemHandle) Write(ctx context.Context, data []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(data) == 0 {
		return 0, nil
	}
	h.batch.mu.Lock()
	item, err := h.itemLocked()
	if err != nil {
		h.batch.mu.Unlock()
		return 0, err
	}
	destPath := item.DestPath
	offset := item.Written
	h.batch.mu.Unlock()
	n, err := h.core.writeStreamingUpload(ctx, destPath, data, offset)
	h.batch.mu.Lock()
	if current := h.batch.byID[h.itemID]; current != nil && !h.closed && n > 0 {
		current.Written += int64(n)
		current.State = task.StateRunning
		current.Error = nil
	}
	h.batch.updateTaskSnapshotLocked()
	h.batch.mu.Unlock()
	return n, err
}

func (h *UploadStreamItemHandle) Commit(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	item, err := h.itemLocked()
	if err != nil {
		h.batch.mu.Unlock()
		return err
	}
	destPath := item.DestPath
	item.Open = false
	item.State = task.StateRunning
	h.batch.updateTaskSnapshotLocked()
	h.batch.mu.Unlock()
	entry, err := h.core.finishStreamingUpload(ctx, destPath)
	if err == nil {
		h.core.refreshUploadStreamCloudProgress(ctx, h.batch)
	}
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	item, itemErr := h.itemLocked()
	if itemErr != nil {
		return itemErr
	}
	if err != nil {
		item.Open = false
		item.State = task.StateWaitingInput
		item.Error = &task.Error{Message: err.Error(), Retryable: true}
		h.closed = true
		h.batch.updateTaskSnapshotLocked()
		return err
	}
	if item.Size == 0 && item.Written > 0 {
		h.batch.bytesTotal += item.Written
		item.Size = item.Written
	}
	item.Open = false
	item.Error = nil
	item.RemoteID = entry.ID
	item.CloudTotal = entry.Size
	if item.CloudPhase == "" || item.CloudPhase == "staging" {
		item.CloudPhase = "queued_upload"
	}
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	_ = entry
	return nil
}

func (h *UploadStreamItemHandle) Fail(code, message string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	item, err := h.itemLocked()
	if err != nil {
		return err
	}
	if message == "" {
		message = "input stream failed"
	}
	item.Open = false
	item.State = task.StateWaitingInput
	item.Error = &task.Error{Code: code, Message: message, Retryable: true}
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	return nil
}

func (h *UploadStreamItemHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	if h.closed {
		return nil
	}
	item := h.batch.byID[h.itemID]
	if item != nil {
		item.Open = false
		if item.State == task.StateRunning {
			item.State = task.StateWaitingInput
		}
	}
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	return nil
}

func (h *UploadStreamItemHandle) itemLocked() (*uploadStreamItem, error) {
	if h.closed {
		return nil, fmt.Errorf("core: upload stream item handle is closed")
	}
	item := h.batch.byID[h.itemID]
	if item == nil {
		return nil, fmt.Errorf("core: upload stream item %q not found", h.itemID)
	}
	if item.State == task.StateCanceled || item.State == task.StateFailed {
		return nil, fmt.Errorf("core: upload stream item %q is %s", h.itemID, item.State)
	}
	return item, nil
}

func (c *Core) cancelUploadStreamItem(ctx context.Context, batch *uploadStreamBatch, itemID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch.mu.Lock()
	if batch.canceled {
		batch.mu.Unlock()
		return fmt.Errorf("core: upload stream task %q is canceled", batch.taskID)
	}
	item := batch.byID[itemID]
	if item == nil {
		batch.mu.Unlock()
		return fmt.Errorf("core: upload stream item %q not found", itemID)
	}
	if item.State == task.StateSucceeded || item.State == task.StateCanceled {
		batch.mu.Unlock()
		return nil
	}
	destPath := item.DestPath
	hadStaging := item.Open || item.Written > 0
	item.Open = false
	item.State = task.StateCanceled
	item.Error = &task.Error{Code: "canceled", Message: "task item canceled"}
	batch.updateTaskSnapshotLocked()
	batch.closeDoneIfTerminalLocked()
	batch.mu.Unlock()
	if !hadStaging {
		return nil
	}
	return c.cancelStreamingUpload(ctx, destPath)
}

func (c *Core) uploadStreamBatchFromRequest(ctx context.Context, req task.Request) (*uploadStreamBatch, error) {
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("core: upload stream task requires at least one item")
	}
	conflictPolicy := normalizeUploadConflictPolicy(req.Options.ConflictPolicy)
	switch conflictPolicy {
	case "", "overwrite", "replace", "fail", "error", "skip":
	default:
		return nil, fmt.Errorf("core: unsupported upload conflict_policy %q", req.Options.ConflictPolicy)
	}
	uploadService, err := c.UploadService()
	if err != nil {
		return nil, err
	}
	batch := &uploadStreamBatch{
		taskID:         newUploadStreamTaskID(),
		byID:           map[string]*uploadStreamItem{},
		conflictPolicy: conflictPolicy,
		ready:          make(chan struct{}),
		done:           make(chan struct{}),
	}
	for i, reqItem := range req.Items {
		destPath := reqItem.DestPath
		if destPath == "" {
			destPath = reqItem.Path
		}
		fallbackName := reqItem.Name
		if fallbackName == "" {
			fallbackName = reqItem.ItemID
		}
		resolvedDestPath, err := c.resolveUploadDestPath(ctx, destPath, fallbackName)
		if err != nil {
			return nil, err
		}
		destPath = resolvedDestPath
		if destPath == "/" {
			return nil, fmt.Errorf("core: upload stream destination must include a file name")
		}
		itemID := reqItem.ItemID
		if itemID == "" {
			itemID = "item-" + strconv.Itoa(i+1)
		}
		if batch.byID[itemID] != nil {
			return nil, fmt.Errorf("core: duplicate upload stream item_id %q", itemID)
		}
		name := reqItem.Name
		if name == "" {
			name = path.Base(destPath)
		}
		if reqItem.Size < 0 {
			return nil, fmt.Errorf("core: upload stream item size must be non-negative")
		}
		item := &uploadStreamItem{
			ID:           itemID,
			DestPath:     destPath,
			Name:         name,
			RelativePath: reqItem.RelativePath,
			Size:         reqItem.Size,
			State:        task.StateWaitingInput,
		}
		if existing, skipped, err := uploadService.applyConflictPolicy(ctx, destPath, conflictPolicy); err != nil {
			item.State = task.StateFailed
			item.Error = &task.Error{Message: err.Error()}
		} else if skipped {
			item.State = task.StateSucceeded
			item.RemoteID = existing.ID
			item.CloudWritten = existing.Size
			item.CloudTotal = existing.Size
			item.CloudPhase = "skipped"
		}
		batch.items = append(batch.items, item)
		batch.byID[itemID] = item
		batch.bytesTotal += reqItem.Size
	}
	batch.mu.Lock()
	batch.closeDoneIfTerminalLocked()
	batch.mu.Unlock()
	return batch, nil
}

func (b *uploadStreamBatch) itemsSnapshot() []uploadStreamItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]uploadStreamItem, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, *item)
	}
	return out
}

func (b *uploadStreamBatch) detailItems() []map[string]any {
	out := make([]map[string]any, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, map[string]any{
			"item_id":       item.ID,
			"dest_path":     item.DestPath,
			"name":          item.Name,
			"relative_path": item.RelativePath,
			"size":          item.Size,
		})
	}
	return out
}

func (b *uploadStreamBatch) resultItemsLocked() []task.ItemResult {
	out := make([]task.ItemResult, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, task.ItemResult{
			Path:              item.DestPath,
			ItemID:            item.ID,
			DestPath:          item.DestPath,
			State:             item.State,
			Phase:             uploadStreamItemPhase(item),
			Error:             cloneTaskError(item.Error),
			RemoteID:          item.RemoteID,
			CloudBytesDone:    item.CloudWritten,
			CloudBytesTotal:   item.CloudTotal,
			StagingBytesDone:  item.Written,
			StagingBytesTotal: item.Size,
			ResumeOffset:      item.Written,
			Capabilities:      uploadStreamItemCapabilities(item),
		})
	}
	return out
}

func uploadStreamItemPhase(item *uploadStreamItem) string {
	if item.CloudPhase != "" {
		return item.CloudPhase
	}
	switch item.State {
	case task.StateWaitingInput:
		return "staging"
	case task.StateRunning:
		return "staging"
	case task.StateSucceeded:
		return "complete"
	case task.StateFailed:
		return "failed"
	case task.StateCanceled:
		return "canceled"
	default:
		return string(item.State)
	}
}

func uploadStreamItemCapabilities(item *uploadStreamItem) task.ItemCapabilities {
	return task.ItemCapabilities{
		OpenInput: item.State == task.StateWaitingInput,
		Cancelable: item.State != task.StateSucceeded &&
			item.State != task.StateFailed &&
			item.State != task.StateCanceled,
	}
}

func (b *uploadStreamBatch) updateTaskSnapshot() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updateTaskSnapshotLocked()
}

func (c *Core) refreshUploadStreamCloudProgress(ctx context.Context, batch *uploadStreamBatch) {
	if c == nil || c.fs == nil {
		return
	}
	source := c.fs.TaskSource()
	items := batch.itemsSnapshot()
	for _, snapshot := range items {
		if snapshot.DestPath == "" {
			continue
		}
		tasks, err := source.ListTasks(ctx, task.Filter{
			Types: []task.Type{task.TypeUploadRemote},
			Path:  snapshot.DestPath,
			Limit: 1,
		})
		if err != nil || len(tasks) == 0 {
			continue
		}
		remote := tasks[0]
		dismissRemote := false
		batch.mu.Lock()
		item := batch.byID[snapshot.ID]
		if item != nil {
			item.CloudWritten = remote.Progress.CloudBytesDone
			item.CloudTotal = remote.Progress.CloudBytesTotal
			item.CloudPhase = remote.Progress.Phase
			if remote.Detail != nil {
				if id, ok := remote.Detail["result_remote_id"].(string); ok {
					item.RemoteID = id
				}
			}
			switch remote.State {
			case task.StateSucceeded:
				item.Open = false
				item.State = task.StateSucceeded
				item.Error = nil
				dismissRemote = true
				if item.CloudPhase == "" {
					item.CloudPhase = "complete"
				}
			case task.StateFailed, task.StateCanceled:
				item.Open = false
				item.State = remote.State
				if remote.Error != nil {
					item.Error = cloneTaskError(remote.Error)
				} else {
					item.Error = &task.Error{Message: fmt.Sprintf("upload %s ended with state %s", snapshot.DestPath, remote.State)}
				}
			}
			batch.closeDoneIfTerminalLocked()
		}
		batch.mu.Unlock()
		if dismissRemote {
			if dismissible, ok := source.(taskDismisser); ok {
				_ = dismissible.DismissTask(context.WithoutCancel(ctx), remote.ID)
			}
		}
	}
}

func (b *uploadStreamBatch) updateTaskSnapshotLocked() {
	if b.update == nil {
		return
	}
	itemsDone, itemsFailed, stagingBytesDone, cloudBytesDone, cloudBytesTotal, phase, active := b.summaryLocked()
	results := b.resultItemsLocked()
	b.update(func(taskItem *task.Task) {
		taskItem.Progress.ItemsDone = itemsDone
		taskItem.Progress.ItemsFailed = itemsFailed
		taskItem.Progress.StagingBytesDone = stagingBytesDone
		taskItem.Progress.StagingBytesTotal = b.bytesTotal
		taskItem.Progress.CloudBytesDone = cloudBytesDone
		taskItem.Progress.CloudBytesTotal = cloudBytesTotal
		taskItem.Progress.Phase = phase
		taskItem.Progress.CurrentPath = ""
		if len(active) > 0 {
			taskItem.Progress.CurrentPath = active[0]
		}
		taskItem.Detail["phase"] = phase
		taskItem.Detail["active_paths"] = active
		taskItem.Result.Items = results
	})
}

func (b *uploadStreamBatch) summaryLocked() (itemsDone, itemsFailed, stagingBytesDone, cloudBytesDone, cloudBytesTotal int64, phase string, active []string) {
	phase = string(task.StateWaitingInput)
	for _, item := range b.items {
		stagingBytesDone += item.Written
		cloudBytesDone += item.CloudWritten
		cloudBytesTotal += item.CloudTotal
		switch item.State {
		case task.StateSucceeded:
			itemsDone++
		case task.StateFailed, task.StateCanceled:
			itemsDone++
			itemsFailed++
		case task.StateRunning:
			phase = "upload"
			active = append(active, item.DestPath)
		}
	}
	if itemsDone == int64(len(b.items)) && itemsFailed == 0 {
		phase = "complete"
	}
	if itemsDone == int64(len(b.items)) && itemsFailed > 0 {
		if itemsFailed == int64(len(b.items)) {
			phase = "failed"
		} else {
			phase = "partial_failed"
		}
	}
	return itemsDone, itemsFailed, stagingBytesDone, cloudBytesDone, cloudBytesTotal, phase, active
}

func (b *uploadStreamBatch) finishTask(update task.UpdateFunc) error {
	b.mu.Lock()
	itemsDone, itemsFailed, _, cloudBytesDone, cloudBytesTotal, _, _ := b.summaryLocked()
	results := b.resultItemsLocked()
	b.mu.Unlock()
	if itemsDone == int64(len(b.items)) && itemsFailed == 0 {
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Progress.CloudBytesDone = cloudBytesDone
			taskItem.Progress.CloudBytesTotal = cloudBytesTotal
			taskItem.Detail["phase"] = "complete"
			taskItem.Detail["active_paths"] = []string{}
			taskItem.Result.Items = results
		})
		return nil
	}
	message := fmt.Sprintf("upload stream failed for %d of %d items", itemsFailed, len(b.items))
	update(func(taskItem *task.Task) {
		taskItem.Progress.CurrentPath = ""
		taskItem.Detail["active_paths"] = []string{}
		taskItem.Error = &task.Error{Message: message, Retryable: true}
		taskItem.Capabilities.Retryable = true
		taskItem.Result.Items = results
		if itemsFailed < int64(len(b.items)) {
			taskItem.State = task.StatePartialFailed
			taskItem.Progress.Phase = "partial_failed"
			taskItem.Detail["phase"] = "partial_failed"
		} else {
			taskItem.Progress.Phase = "failed"
			taskItem.Detail["phase"] = "failed"
		}
	})
	if itemsFailed < int64(len(b.items)) {
		return nil
	}
	return fmt.Errorf("%s", message)
}

func (b *uploadStreamBatch) markCanceled(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.canceled = true
	for _, item := range b.items {
		if item.State == task.StateSucceeded || item.State == task.StateFailed {
			continue
		}
		item.Open = false
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "canceled", Message: err.Error()}
	}
}

func (b *uploadStreamBatch) closeDoneIfTerminalLocked() {
	for _, item := range b.items {
		if item.State != task.StateSucceeded && item.State != task.StateFailed && item.State != task.StateCanceled {
			return
		}
	}
	b.doneOnce.Do(func() { close(b.done) })
}

func (c *Core) putUploadStream(batch *uploadStreamBatch) {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	if c.uploadStreams == nil {
		c.uploadStreams = map[string]*uploadStreamBatch{}
	}
	c.uploadStreams[batch.taskID] = batch
}

func (c *Core) getUploadStream(taskID string) *uploadStreamBatch {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	return c.uploadStreams[taskID]
}

func (c *Core) removeUploadStream(taskID string) {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	delete(c.uploadStreams, taskID)
}

func newUploadStreamTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "upload-stream-" + strconv.FormatInt(util.Now().UnixNano(), 36)
	}
	return "upload-stream-" + hex.EncodeToString(b[:])
}
